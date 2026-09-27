// Package tui is the interactive Bubble Tea front end.
//
// Finished output (messages, tool cards) is printed into the terminal's own
// scrollback with tea.Println; the live view only holds what is still
// changing: the streaming reply, running tools, prompts, input and status.
package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"

	"larik/internal/agent"
	"larik/internal/app"
	"larik/internal/config"
	"larik/internal/hooks"
	"larik/internal/llm"
	"larik/internal/lsp"
	"larik/internal/mcp"
	"larik/internal/sandbox"
	"larik/internal/skills"
	"larik/internal/subagent"
)

type Options struct {
	// App and Session enable switching sessions from the TUI; the TUI
	// then owns the session and closes whichever is current on exit.
	App     *app.App
	Session *app.Session

	Agent         *agent.Agent
	Config        *config.Config
	InitialPrompt string
	History       []llm.Message // shown when resuming
	SessionDir    string
	Version       string
	MCP           *mcp.Manager
	Hooks         *hooks.Runner
	Skills        *skills.Set
	Agents        *subagent.Set
	LSP           *lsp.Manager
	Sandbox       *sandbox.Sandbox // nil when unavailable or disabled
	SandboxNote   string           // why there is no sandbox, if any
	SearchNote    string           // why web_search is unavailable, if configured but broken
}

func Run(opts Options) error {
	if opts.Session != nil {
		opts.Agent, opts.Hooks, opts.History = opts.Session.Agent, opts.Session.Hooks, opts.Session.History
	}
	m := newModel(opts)
	_, err := tea.NewProgram(m).Run()
	if m.sess != nil {
		m.sess.Close("prompt_input_exit")
	}
	return err
}

type toolRun struct {
	id, name string
	input    []byte
	agent    string // subagent label, if any
}

type model struct {
	opts   Options
	agent  *agent.Agent
	sess   *app.Session  // nil when the caller manages the session
	bgStop chan struct{} // closed when switching away from agent
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
	perm      *agent.Event  // permission prompt being shown
	permQueue []agent.Event // further prompts (parallel subagents)
	// bgReplies marks prompts from background tasks, which must survive
	// the end of the foreground turn.
	bgReplies map[chan<- agent.PermissionReply]bool
	permIdx   int
	queue     []string
	stats     agent.UsageInfo
	busyLabel string // non-agent background work, e.g. compaction
	quitArmed bool

	mpick      *modelPicker              // the /model dropdown, when open
	modelLists map[string]providerModels // last model lists, by provider
	wizard     *wizard                   // the connect wizard, when open
}

// Messages.
type (
	agentEventMsg agent.Event
	bgEventMsg    struct {
		agent.Event
		from *agent.Agent
	}
	runEndedMsg  struct{}
	compactedMsg struct {
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
		opts:   opts,
		agent:  opts.Agent,
		sess:   opts.Session,
		bgStop: make(chan struct{}),
		input:  ta,
		spin:   spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		width:  80,
		stats:  opts.Agent.Stats(),
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
		cmds = append(cmds, m.printHistory("resumed"))
	}
	if p := strings.TrimSpace(m.opts.InitialPrompt); p != "" {
		cmds = append(cmds, m.submit(p))
	}
	// The background listener blocks until an event arrives, so it runs
	// alongside the startup sequence rather than inside it.
	return tea.Batch(m.waitBackground(), tea.Sequence(cmds...))
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
		m.running, m.cancel, m.events = false, nil, nil
		m.dropForegroundPerms()
		m.resetStream()
		m.stats = m.agent.Stats()
		if len(m.queue) > 0 { // pending background results ride along with it
			next := m.queue[0]
			m.queue = m.queue[1:]
			return m, m.submit(next)
		}
		return m, m.deliverBackground()

	case bgEventMsg:
		if msg.from != m.agent {
			return m, nil // from a session we switched away from
		}
		e := msg.Event
		var cmds []tea.Cmd
		switch e.Kind {
		case agent.EvTaskDone:
			cmds = append(cmds, m.println(m.renderTaskDone(e)))
			if !m.running {
				cmds = append(cmds, m.deliverBackground())
			}
		case agent.EvPermission:
			if m.bgReplies == nil {
				m.bgReplies = map[chan<- agent.PermissionReply]bool{}
			}
			m.bgReplies[e.Reply] = true
			cmds = append(cmds, m.handleEvent(e))
		default:
			cmds = append(cmds, m.handleEvent(e))
		}
		// Re-arm the listener concurrently: it blocks until the next
		// background event, so it must not sit in the sequence.
		return m, tea.Batch(m.waitBackground(), tea.Sequence(cmds...))

	case compactedMsg:
		m.busyLabel = ""
		m.stats = m.agent.Stats()
		if msg.err != nil {
			return m, m.println(m.st.err.Render("compaction failed: " + msg.err.Error()))
		}
		return m, m.println(m.st.dim.Render("✓ Conversation compacted. Summary:\n") + m.st.dim.Render(truncateLines(msg.summary, 12)))

	case modelsLoadedMsg:
		m.modelLists = msg.lists
		if m.mpick != nil {
			m.mpick.loading = false
			m.buildModelList()
		}
		return m, nil

	case wizDetectedMsg, wizTestedMsg:
		if m.wizard == nil {
			return m, nil
		}
		return m, m.handleWizard(msg)

	case tea.KeyPressMsg:
		switch {
		case m.perm != nil:
			return m, m.handlePermissionKey(msg)
		case m.wizard != nil:
			return m, m.handleWizard(msg)
		case m.mpick != nil:
			return m, m.handleModelPickerKey(msg)
		}
		return m.handleKey(msg)
	}

	if m.wizard != nil { // e.g. cursor blinks for its text fields
		return m, m.handleWizard(msg)
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
	case "alt+p":
		if m.running {
			return m, m.println(m.st.err.Render("the model can't change while a turn is running (esc to interrupt)"))
		}
		return m, m.openModelPicker()
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
		m.tools = append(m.tools, toolRun{id: e.ToolID, name: e.ToolName, input: e.Input, agent: e.Agent})
	case agent.EvToolEnd:
		for i, t := range m.tools {
			if t.id == e.ToolID && t.agent == e.Agent { // child ids can repeat the parent's
				m.tools = append(m.tools[:i], m.tools[i+1:]...)
				break
			}
		}
		return m.println(m.renderToolCard(e))
	case agent.EvPermission:
		if m.perm != nil {
			m.permQueue = append(m.permQueue, e)
			return nil
		}
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
	delete(m.bgReplies, ev.Reply)
	if len(m.permQueue) > 0 {
		next := m.permQueue[0]
		m.permQueue = m.permQueue[1:]
		m.perm, m.permIdx = &next, 0
	}
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

// waitBackground reads the agent's background-task stream for its lifetime.
func (m *model) waitBackground() tea.Cmd {
	a, stop := m.agent, m.bgStop
	ch := a.Background()
	return func() tea.Msg {
		select {
		case e := <-ch:
			return bgEventMsg{e, a}
		case <-stop:
			return nil
		}
	}
}

// deliverBackground starts a turn that hands finished background results
// to the model, if any are waiting and nothing else is running.
func (m *model) deliverBackground() tea.Cmd {
	if m.running || m.agent.PendingNotifications() == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	events, ok := m.agent.RunNotifications(ctx)
	if !ok {
		cancel()
		return nil
	}
	m.running, m.cancel, m.events = true, cancel, events
	return tea.Sequence(
		m.println("\n"+m.st.dim.Render("⚙ delivering background task results to the model")),
		tea.Batch(m.waitEvent(), m.spin.Tick),
	)
}

// dropForegroundPerms discards prompts from a finished turn but keeps
// those from background tasks, which are still waiting for an answer.
func (m *model) dropForegroundPerms() {
	keep := func(e *agent.Event) bool { return e != nil && m.bgReplies[e.Reply] }
	var queue []agent.Event
	for i := range m.permQueue {
		if keep(&m.permQueue[i]) {
			queue = append(queue, m.permQueue[i])
		}
	}
	if !keep(m.perm) {
		m.perm = nil
		if len(queue) > 0 {
			m.perm, queue = &queue[0], queue[1:]
			m.permIdx = 0
		}
	}
	m.permQueue = queue
}

func (m *model) renderTaskDone(e agent.Event) string {
	mark := m.st.ok.Render("◆")
	if e.IsError {
		mark = m.st.err.Render("◆")
	}
	head := fmt.Sprintf("%s background %s %s  %s", mark, e.ToolID, e.StopReason, m.st.dim.Render("("+e.Agent+")"))
	body := strings.TrimSpace(e.Output)
	if body == "" {
		return head
	}
	return head + "\n" + prefixLines(m.st.dim.Render(truncateLines(body, 4)), "  ⎿ ", "    ")
}
