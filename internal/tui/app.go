// Package tui is the interactive Bubble Tea front end.
//
// Finished output (messages, tool cards) is printed into the terminal's own
// scrollback with tea.Println; the live view only holds what is still
// changing: the streaming reply, running tools, prompts, input and status.
package tui

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"

	"larik/internal/agent"
	"larik/internal/config"
	"larik/internal/llm"
	"larik/internal/mcp"
)

type Options struct {
	Agent         *agent.Agent
	Config        *config.Config
	InitialPrompt string
	History       []llm.Message // shown when resuming
	SessionDir    string
	Version       string
	MCP           *mcp.Manager
}

func Run(opts Options) error {
	m := newModel(opts)
	_, err := tea.NewProgram(m).Run()
	return err
}

type toolRun struct {
	id, name string
	input    []byte
}

type model struct {
	opts   Options
	agent  *agent.Agent
	st     styles
	md     *glamour.TermRenderer
	isDark bool
	width  int
	height int

	input textarea.Model
	spin  spinner.Model

	running   bool
	cancel    context.CancelFunc
	events    <-chan agent.Event
	stream    strings.Builder // assistant text of the in-flight response
	thinking  strings.Builder
	calling   string // tool the model is currently writing a call for
	tools     []toolRun
	perm      *agent.Event
	permIdx   int
	queue     []string
	stats     agent.UsageInfo
	busyLabel string // non-agent background work, e.g. compaction
	quitArmed bool
}

// Messages.
type (
	agentEventMsg agent.Event
	runEndedMsg   struct{}
	compactedMsg  struct {
		summary string
		err     error
	}
)

func newModel(opts Options) *model {
	// Assume dark until the terminal answers RequestBackgroundColor; querying
	// synchronously before startup would block and swallow typed input.
	isDark := true
	ta := textarea.New()
	ta.Placeholder = "Ask Larik to do something…  (/help for commands)"
	ta.ShowLineNumbers = false
	ta.Prompt = "› "
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = 10
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "alt+enter", "ctrl+j"))
	ta.Focus()

	m := &model{
		opts:  opts,
		agent: opts.Agent,
		input: ta,
		spin:  spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		width: 80,
		stats: opts.Agent.Stats(),
	}
	m.applyTheme(isDark)
	return m
}

func (m *model) applyTheme(isDark bool) {
	m.isDark = isDark
	m.st = newStyles(isDark)
	m.input.SetStyles(textarea.DefaultStyles(isDark))
	m.setWidth(m.width)
}

func (m *model) setWidth(w int) {
	m.width = w
	m.input.SetWidth(max(w-4, 10))
	style := "dark"
	if !m.isDark {
		style = "light"
	}
	m.md, _ = glamour.NewTermRenderer(glamour.WithStandardStyle(style), glamour.WithWordWrap(max(w-4, 20)))
}

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.RequestBackgroundColor, m.printBanner()}
	if len(m.opts.History) > 0 {
		cmds = append(cmds, m.printHistory())
	}
	if p := strings.TrimSpace(m.opts.InitialPrompt); p != "" {
		cmds = append(cmds, m.submit(p))
	}
	return tea.Sequence(cmds...)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		if msg.IsDark() != m.isDark {
			m.applyTheme(msg.IsDark())
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.height = msg.Height
		m.setWidth(msg.Width)
		return m, nil

	case spinner.TickMsg:
		if !m.running && m.busyLabel == "" {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case agentEventMsg:
		return m, tea.Batch(m.handleEvent(agent.Event(msg)), m.waitEvent())

	case runEndedMsg:
		m.running, m.cancel, m.events, m.perm = false, nil, nil, nil
		m.resetStream()
		m.stats = m.agent.Stats()
		if len(m.queue) > 0 {
			next := m.queue[0]
			m.queue = m.queue[1:]
			return m, m.submit(next)
		}
		return m, nil

	case compactedMsg:
		m.busyLabel = ""
		m.stats = m.agent.Stats()
		if msg.err != nil {
			return m, m.println(m.st.err.Render("compaction failed: " + msg.err.Error()))
		}
		return m, m.println(m.st.dim.Render("✓ Conversation compacted. Summary:\n") + m.st.dim.Render(truncateLines(msg.summary, 12)))

	case tea.KeyPressMsg:
		if m.perm != nil {
			return m, m.handlePermissionKey(msg)
		}
		return m.handleKey(msg)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if k != "ctrl+c" {
		m.quitArmed = false
	}
	switch k {
	case "ctrl+c":
		switch {
		case m.running:
			m.interrupt()
			return m, nil
		case m.input.Value() != "":
			m.input.Reset()
			return m, nil
		case m.quitArmed:
			return m, tea.Quit
		}
		m.quitArmed = true
		return m, nil
	case "ctrl+d":
		if m.input.Value() == "" {
			return m, tea.Quit
		}
	case "esc":
		if m.running {
			m.interrupt()
			return m, nil
		}
	case "shift+tab":
		return m, m.cycleMode()
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
		}
		m.input.Reset()
		if strings.HasPrefix(text, "/") {
			return m, m.command(text)
		}
		if m.running {
			m.queue = append(m.queue, text)
			return m, nil
		}
		return m, m.submit(text)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) interrupt() {
	if m.cancel != nil {
		m.cancel()
	}
	m.queue = nil
}

// submit echoes the prompt and starts an agent run.
func (m *model) submit(text string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.running, m.cancel = true, cancel
	m.events = m.agent.Run(ctx, text)
	return tea.Sequence(
		m.println("\n"+m.st.user.Render("› "+indentAfterFirst(text, "  "))),
		tea.Batch(m.waitEvent(), m.spin.Tick),
	)
}

func (m *model) waitEvent() tea.Cmd {
	ch := m.events
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return runEndedMsg{}
		}
		return agentEventMsg(e)
	}
}

func (m *model) resetStream() {
	m.stream.Reset()
	m.thinking.Reset()
	m.calling = ""
	m.tools = nil
}

func (m *model) handleEvent(e agent.Event) tea.Cmd {
	switch e.Kind {
	case agent.EvTextDelta:
		m.stream.WriteString(e.Text)
	case agent.EvThinkingDelta:
		m.thinking.WriteString(e.Text)
	case agent.EvToolCallDelta:
		m.calling = e.ToolName
	case agent.EvAssistant:
		out := m.renderAssistant(*e.Message)
		m.stream.Reset()
		m.thinking.Reset()
		m.calling = ""
		if out == "" {
			return nil
		}
		return m.println(out)
	case agent.EvToolStart:
		m.tools = append(m.tools, toolRun{id: e.ToolID, name: e.ToolName, input: e.Input})
	case agent.EvToolEnd:
		for i, t := range m.tools {
			if t.id == e.ToolID {
				m.tools = append(m.tools[:i], m.tools[i+1:]...)
				break
			}
		}
		return m.println(m.renderToolCard(e))
	case agent.EvPermission:
		ev := e
		m.perm, m.permIdx = &ev, 0
	case agent.EvUsage:
		m.stats = *e.Usage
	case agent.EvCompacted:
		return m.println(m.st.dim.Render("✓ Context compacted to stay within the model's window."))
	case agent.EvNotice:
		return m.println(m.st.warn.Render("! " + e.Text))
	case agent.EvError:
		return m.println(m.st.err.Render("✗ " + e.Text))
	case agent.EvDone:
		if e.StopReason == "interrupted" {
			var cmds []tea.Cmd
			if partial := strings.TrimSpace(m.stream.String()); partial != "" {
				cmds = append(cmds, m.println(m.renderMarkdown(partial)))
			}
			cmds = append(cmds, m.println(m.st.warn.Render("⏹ Interrupted")))
			m.stream.Reset()
			return tea.Sequence(cmds...)
		}
	}
	return nil
}

func (m *model) handlePermissionKey(msg tea.KeyPressMsg) tea.Cmd {
	const options = 3
	var reply *agent.PermissionReply
	switch msg.String() {
	case "up", "k", "shift+tab":
		m.permIdx = (m.permIdx + options - 1) % options
	case "down", "j", "tab":
		m.permIdx = (m.permIdx + 1) % options
	case "1", "y":
		reply = &agent.PermissionReply{Allow: true}
	case "2", "a":
		reply = &agent.PermissionReply{Allow: true, Always: true}
	case "3", "n", "esc":
		reply = &agent.PermissionReply{Allow: false}
	case "ctrl+c":
		reply = &agent.PermissionReply{Allow: false}
		m.interrupt()
	case "enter":
		reply = &[]agent.PermissionReply{{Allow: true}, {Allow: true, Always: true}, {Allow: false}}[m.permIdx]
	}
	if reply == nil {
		return nil
	}
	ev := m.perm
	m.perm = nil
	ev.Reply <- *reply
	if !reply.Allow {
		return m.println(m.st.dim.Render("  ⎿ denied " + toolTitle(ev.ToolName, ev.Input)))
	}
	if reply.Always {
		return m.println(m.st.dim.Render("  ⎿ always allowing " + ev.SuggestedRule + " (saved to .larik/settings.local.json)"))
	}
	return nil
}

func (m *model) println(s string) tea.Cmd {
	return tea.Println(s)
}
