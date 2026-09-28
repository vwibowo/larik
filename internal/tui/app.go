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
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"

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
	started  time.Time
}

type model struct {
	opts   Options
	agent  *agent.Agent
	sess   *app.Session  // nil when the caller manages the session
	bgStop chan struct{} // closed when switching away from agent
	st     styles
	md     *glamour.TermRenderer
	isDark bool
	// termDark is what the terminal reported; the theme setting may
	// override it.
	termDark bool
	width    int
	height   int

	input textarea.Model
	spin  spinner.Model

	running      bool
	cancel       context.CancelFunc
	events       <-chan agent.Event
	stream       strings.Builder // assistant text of the in-flight response
	thinking     strings.Builder
	calling      string // tool the model is currently writing a call for
	tools        []toolRun
	taskCalls    map[string]int  // completed calls by active subagent label
	perm         *agent.Event    // permission prompt being shown
	permQueue    []agent.Event   // further prompts (parallel subagents)
	permFeedback *textarea.Model // denial feedback, when the third option is selected
	// bgReplies marks prompts from background tasks, which must survive
	// the end of the foreground turn.
	bgReplies map[chan<- agent.PermissionReply]bool
	permIdx   int
	queue     []string
	stats     agent.UsageInfo
	turnStart time.Time // when the running turn began
	turnChars int       // text and thinking streamed this turn, for a token estimate
	// thinkStart is when the in-flight message began thinking, and
	// thinkDur how long it thought once it moved on.
	thinkStart   time.Time
	thinkDur     time.Duration
	showThinking bool   // ctrl+o: show thinking in full instead of one line
	lastThinking string // thinking of the last finished message, for ctrl+o
	busyLabel    string // non-agent background work, e.g. compaction
	quitArmed    bool

	mpick       *modelPicker              // the /model dropdown, when open
	modelLists  map[string]providerModels // last model lists, by provider
	wizard      *wizard                   // the connect wizard, when open
	routing     *routingWizard            // the /routing wizard, when open
	modePick    *picker                   // the /mode dropdown, when open
	sessionPick *picker                   // the /sessions and /resume dropdown
	provs       *providerManager          // the /providers screen, when open
	// wizardReturn reopens /providers when a wizard started there closes.
	wizardReturn bool
	showKeys     bool           // the ? shortcuts overlay
	settings     *settingsPanel // the /config screen, when open

	// Settings from /config.
	verbose bool   // tool output in full
	tips    bool   // a tip under the spinner
	tip     string // the tip for the running turn
	notify  string // off, bell or desktop
	focused bool   // terminal has focus; notifications only go out without it
	// The command palette shows while a bare "/name" is being typed.
	palette       *picker
	paletteQ      string // input the palette was built for
	paletteHidden string // input the palette was dismissed at with esc
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
	const termDark = true
	ta := textarea.New()
	ta.Placeholder = "Ask larik to do something…  (/ for commands)"
	ta.ShowLineNumbers = false
	ta.Prompt = "› "
	ta.MaxWidth = 0 // allow the composer to follow the terminal at any width
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

		termDark: termDark,
		focused:  true,
		tips:     true,
		notify:   "off",
	}
	if c := opts.Config; c != nil {
		m.verbose, m.tips, m.notify = c.VerboseOn(), c.TipsOn(), c.Notifications
		if m.notify == "" {
			m.notify = "off"
		}
	}
	m.showThinking = m.verbose
	m.applyTheme(m.wantDark())
	return m
}

// theme is the theme setting: auto, dark or light.
func (m *model) theme() string {
	if m.opts.Config == nil || m.opts.Config.Theme == "" {
		return "auto"
	}
	return m.opts.Config.Theme
}

// wantDark resolves the theme setting against the terminal's background.
func (m *model) wantDark() bool {
	switch m.theme() {
	case "dark":
		return true
	case "light":
		return false
	}
	return m.termDark
}

func (m *model) applyTheme(isDark bool) {
	m.isDark = isDark
	m.st = newStyles(isDark)
	inputStyles := textarea.DefaultStyles(isDark)
	if isDark {
		inputStyles.Focused.CursorLine = inputStyles.Focused.CursorLine.Background(lipgloss.Color("#202024"))
	} else {
		inputStyles.Focused.CursorLine = inputStyles.Focused.CursorLine.Background(lipgloss.Color("#F0F0F2"))
	}
	m.input.SetStyles(inputStyles)
	if m.permFeedback != nil {
		m.permFeedback.SetStyles(textarea.DefaultStyles(isDark))
	}
	m.setWidth(m.width)
}

func (m *model) setWidth(w int) {
	m.width = w
	m.input.SetWidth(max(w-4, 4)) // the composer border and padding use four cells
	if m.permFeedback != nil {
		m.permFeedback.SetWidth(max(w-10, 10))
	}
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
		m.termDark = msg.IsDark()
		if d := m.wantDark(); d != m.isDark {
			m.applyTheme(d)
		}
		return m, nil

	case tea.FocusMsg:
		m.focused = true
		return m, nil

	case tea.BlurMsg:
		m.focused = false
		return m, nil

	case tea.WindowSizeMsg:
		m.height = msg.Height
		m.setWidth(msg.Width)
		return m, nil

	case spinner.TickMsg:
		if !m.running && m.busyLabel == "" && m.agent.RunningBackground() == 0 {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case agentEventMsg:
		return m, tea.Batch(m.handleEvent(agent.Event(msg)), m.waitEvent())

	case runEndedMsg:
		var alert tea.Cmd
		if time.Since(m.turnStart) >= longTurn {
			alert = m.alert("finished")
		}
		m.running, m.cancel, m.events = false, nil, nil
		m.dropForegroundPerms()
		m.resetStream()
		m.stats = m.agent.Stats()
		if len(m.queue) > 0 { // pending background results ride along with it
			next := m.queue[0]
			m.queue = m.queue[1:]
			return m, m.submit(next)
		}
		return m, tea.Batch(alert, m.deliverBackground())

	case bgEventMsg:
		if msg.from != m.agent {
			return m, nil // from a session we switched away from
		}
		e := msg.Event
		var cmds []tea.Cmd
		switch e.Kind {
		case agent.EvTaskDone:
			m.clearTaskCalls(e.Agent)
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
		previous := m.agent.Effort()
		if m.mpick != nil {
			levels := m.mpick.selectedEfforts(m)
			previous = levels[min(m.mpick.effort, len(levels)-1)]
		}
		m.modelLists = msg.lists
		if m.routing != nil {
			m.routing.setLists(msg.lists)
		}
		if m.mpick != nil {
			m.mpick.loading = false
			m.buildModelList()
			m.mpick.syncEffort(m, previous)
		}
		if m.provs != nil {
			m.buildProviders()
		}
		return m, nil

	case providerTestedMsg:
		if m.modelLists == nil {
			m.modelLists = map[string]providerModels{}
		}
		m.modelLists[msg.name] = msg.res
		if m.provs != nil {
			delete(m.provs.testing, msg.name)
			m.buildProviders()
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
		case m.showKeys:
			m.showKeys = false // any key closes it
			return m, nil
		case m.wizard != nil:
			return m, m.handleWizard(msg)
		case m.routing != nil:
			return m, m.handleRouting(msg)
		case m.mpick != nil:
			return m, m.handleModelPickerKey(msg)
		case m.modePick != nil:
			return m, m.handleModePickerKey(msg)
		case m.sessionPick != nil:
			return m, m.handleSessionPickerKey(msg)
		case m.settings != nil:
			return m, m.handleSettingsKey(msg)
		case m.provs != nil:
			return m, m.handleProvidersKey(msg)
		case m.palette != nil:
			if cmd, ok := m.handlePaletteKey(msg); ok {
				return m, cmd
			}
		}
		md, cmd := m.handleKey(msg)
		m.syncPalette()
		return md, cmd
	}

	if m.wizard != nil { // e.g. cursor blinks for its text fields
		return m, m.handleWizard(msg)
	}
	if w := m.routing; w != nil && w.step == rtBudget {
		var cmd tea.Cmd
		w.fields[w.focus], cmd = w.fields[w.focus].Update(msg)
		return m, cmd
	}
	if m.permFeedback != nil {
		var cmd tea.Cmd
		*m.permFeedback, cmd = m.permFeedback.Update(msg)
		return m, cmd
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.syncPalette() // e.g. after a paste
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
	case "ctrl+o":
		return m, m.toggleThinking()
	case "?":
		if m.input.Value() == "" {
			m.showKeys = true
			return m, nil
		}
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
	m.turnStart, m.turnChars = time.Now(), 0
	m.tip = nextTip()
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
	m.thinkStart, m.thinkDur = time.Time{}, 0
}

// doneThinking records how long the in-flight message thought, once it
// starts answering or calling a tool.
func (m *model) doneThinking() {
	if !m.thinkStart.IsZero() && m.thinkDur == 0 {
		m.thinkDur = time.Since(m.thinkStart)
	}
}

// toggleThinking switches between one-line and full thinking. Turning it
// on while idle also prints the last message's thinking.
func (m *model) toggleThinking() tea.Cmd {
	m.showThinking = !m.showThinking
	if !m.showThinking {
		return m.println(m.st.dim.Render("thinking collapsed (ctrl+o to show)"))
	}
	if !m.running && m.lastThinking != "" {
		return m.println(m.st.thinking.Render("✻ " + truncateLines(wrap(m.lastThinking, m.width-4), 40)))
	}
	return m.println(m.st.dim.Render("thinking shown (ctrl+o to collapse)"))
}

func (m *model) handleEvent(e agent.Event) tea.Cmd {
	switch e.Kind {
	case agent.EvTextDelta:
		m.doneThinking()
		m.stream.WriteString(e.Text)
		m.turnChars += len(e.Text)
	case agent.EvThinkingDelta:
		if m.thinkStart.IsZero() {
			m.thinkStart = time.Now()
		}
		m.thinking.WriteString(e.Text)
		m.turnChars += len(e.Text)
	case agent.EvToolCallDelta:
		m.doneThinking()
		m.calling = e.ToolName
	case agent.EvAssistant:
		m.doneThinking()
		out := m.renderAssistant(*e.Message, m.thinkDur)
		for _, b := range e.Message.Blocks {
			if b.Type == llm.BlockThinking && strings.TrimSpace(b.Text) != "" {
				m.lastThinking = strings.TrimSpace(b.Text)
			}
		}
		m.stream.Reset()
		m.thinking.Reset()
		m.calling = ""
		m.thinkStart, m.thinkDur = time.Time{}, 0
		if out == "" {
			return nil
		}
		return m.println(out)
	case agent.EvToolStart:
		m.tools = append(m.tools, toolRun{id: e.ToolID, name: e.ToolName, input: e.Input, agent: e.Agent, started: time.Now()})
	case agent.EvToolEnd:
		if e.Agent != "" {
			if m.taskCalls == nil {
				m.taskCalls = make(map[string]int)
			}
			m.taskCalls[e.Agent]++
		} else if e.ToolName == "task" && !taskIsBackground(e.Input) {
			m.clearTaskCalls(taskLabel(e.Input))
		}
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
		return m.alert("needs your permission to use " + e.ToolName)
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
	if m.permFeedback != nil {
		switch msg.String() {
		case "enter":
			return m.replyPermission(agent.PermissionReply{Allow: false, Reason: strings.TrimSpace(m.permFeedback.Value())})
		case "esc":
			return m.replyPermission(agent.PermissionReply{Allow: false})
		case "ctrl+c":
			m.interrupt()
			return m.replyPermission(agent.PermissionReply{Allow: false})
		}
		var cmd tea.Cmd
		*m.permFeedback, cmd = m.permFeedback.Update(msg)
		return cmd
	}
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
	case "3", "n":
		m.startPermissionFeedback()
		return nil
	case "esc":
		reply = &agent.PermissionReply{Allow: false}
	case "ctrl+c":
		reply = &agent.PermissionReply{Allow: false}
		m.interrupt()
	case "enter":
		if m.permIdx == 2 {
			m.startPermissionFeedback()
			return nil
		}
		reply = &[]agent.PermissionReply{{Allow: true}, {Allow: true, Always: true}}[m.permIdx]
	}
	if reply == nil {
		return nil
	}
	return m.replyPermission(*reply)
}

func (m *model) startPermissionFeedback() {
	ta := textarea.New()
	ta.Placeholder = "What should larik do instead?"
	ta.ShowLineNumbers = false
	ta.Prompt = "› "
	ta.DynamicHeight = true
	ta.MinHeight, ta.MaxHeight = 1, 4
	ta.SetWidth(max(m.width-10, 10))
	ta.SetStyles(textarea.DefaultStyles(m.isDark))
	ta.Focus()
	m.permFeedback = &ta
}

func (m *model) replyPermission(reply agent.PermissionReply) tea.Cmd {
	ev := m.perm
	m.perm = nil
	m.permFeedback = nil
	delete(m.bgReplies, ev.Reply)
	if len(m.permQueue) > 0 {
		next := m.permQueue[0]
		m.permQueue = m.permQueue[1:]
		m.perm, m.permIdx = &next, 0
	}
	ev.Reply <- reply
	if !reply.Allow {
		return m.println(m.st.dim.Render("  ⎿ denied " + toolTitle(ev.ToolName, ev.Input, m.shortPaths)))
	}
	if reply.Always {
		return m.println(m.st.dim.Render("  ⎿ won't ask again for " + ev.SuggestedRule + " this session"))
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
	m.turnStart, m.turnChars = time.Now(), 0
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
		m.permFeedback = nil
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
	body := strings.TrimSpace(m.shortPaths(e.Output))
	if body == "" {
		return head
	}
	return head + "\n" + prefixLines(m.st.dim.Render(truncateLines(body, 4)), "  ⎿ ", "    ")
}
