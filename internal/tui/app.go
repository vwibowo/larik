// Package tui is the interactive Bubble Tea front end.
//
// Finished output and live activity share a scrollable conversation area.
// The composer and status stay fixed below it.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"

	"larik/internal/agent"
	"larik/internal/app"
	"larik/internal/audio"
	"larik/internal/config"
	"larik/internal/hooks"
	"larik/internal/llm"
	"larik/internal/lsp"
	"larik/internal/mcp"
	"larik/internal/memory"
	"larik/internal/permission"
	"larik/internal/sandbox"
	"larik/internal/skills"
	"larik/internal/subagent"
	"larik/internal/tools"
	"larik/internal/trace/viewer"
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
	Memory        *memory.Store // nil when memory is off
	Agents        *subagent.Set
	LSP           *lsp.Manager
	Sandbox       *sandbox.Sandbox // nil when unavailable or disabled
	SandboxNote   string           // why there is no sandbox, if any
	SearchNote    string           // why web_search is unavailable, if configured but broken
	BrowserNote   string           // why browser tools are unavailable, if enabled but Chrome cannot launch
	Audio         audio.Service
}

func Run(opts Options) error {
	if opts.Session != nil {
		opts.Agent, opts.Hooks, opts.History = opts.Session.Agent, opts.Session.Hooks, opts.Session.History
	}
	m := newModel(opts)
	_, err := tea.NewProgram(m).Run()
	if m.recording != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = m.recording.Stop(ctx)
		cancel()
		m.recording = nil
	}
	if m.traceView != nil {
		m.traceView.Close()
	}
	if m.sess != nil {
		m.sess.Close("prompt_input_exit")
	}
	for _, a := range m.ownedApps {
		a.Close()
	}
	// The alt screen takes the conversation with it; say how to get back.
	if id := m.agent.SessionID(); id != "" && m.prompted {
		fmt.Printf("session %s · resume with: larik --resume %s\n", id, id)
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
	opts      Options
	agent     *agent.Agent
	sess      *app.Session  // nil when the caller manages the session
	bgStop    chan struct{} // closed when switching away from agent
	ownedApps []*app.App    // apps created by in-TUI reload; initial app belongs to caller
	st        styles
	md        *glamour.TermRenderer
	isDark    bool
	// termDark is what the terminal reported; the theme setting may
	// override it.
	termDark bool
	width    int
	height   int

	input         textarea.Model
	keys          keymap   // the keybindings setting applied to the defaults
	keyWarn       []string // keybindings that couldn't apply, for the banner
	status        *statusCmd
	sidebarStatus *statusCmd
	vim           *vimEditor // nil unless editor_mode is vim
	// traceView serves traceDir to the browser once /trace opened it.
	traceView *viewer.Server
	traceDir  string
	spin      spinner.Model
	view      viewport.Model // the conversation, pre-wrapped to width
	panelView viewport.Model
	panelKind string
	// outputs is everything printed, oldest first, kept to re-wrap on
	// resize; convLines is outputs wrapped to convWidth.
	outputs     []string
	outputBytes int
	convLines   []string
	convWidth   int
	// Layout of the last frame, for routing mouse wheel events.
	panelTop, panelRows int
	// frameBottomRows caches the composer and status height while a
	// frame renders; zero outside View.
	frameBottomRows int
	prompted        bool // a prompt was sent, so the session is worth resuming

	running      bool
	cancel       context.CancelFunc
	events       <-chan agent.Event
	stream       strings.Builder // assistant text of the in-flight response
	thinking     strings.Builder
	calling      string // tool the model is currently writing a call for
	tools        []toolRun
	taskCalls    map[string]int    // completed calls by active subagent label
	taskModels   map[string]string // model each active subagent runs on, by label
	perm         *agent.Event      // permission prompt being shown
	permQueue    []agent.Event     // further prompts (parallel subagents)
	permFeedback *textarea.Model   // denial feedback, when the third option is selected
	// bgReplies marks prompts from background tasks, which must survive
	// the end of the foreground turn.
	bgReplies      map[chan<- agent.PermissionReply]bool
	permIdx        int
	queue          []string
	stats          agent.UsageInfo
	turnStats      turnStats
	projectRoot    string
	project        projectInfo
	projectLoading bool
	turnStart      time.Time // when the running turn began
	turnChars      int       // text and thinking streamed this turn, for a token estimate
	// thinkStart is when the in-flight message began thinking, and
	// thinkDur how long it thought once it moved on.
	thinkStart   time.Time
	thinkDur     time.Duration
	showThinking bool   // ctrl+o: show thinking in full instead of one line
	lastThinking string // thinking of the last finished message, for ctrl+o
	lastReply    string // text of the last reply, for /copy
	busyLabel    string // non-agent background work, e.g. compaction
	quitArmed    bool

	mpick       *modelPicker              // the /model dropdown, when open
	modelLists  map[string]providerModels // last model lists, by provider
	wizard      *wizard                   // the connect wizard, when open
	routing     *routingWizard            // the /routing wizard, when open
	modePick    *picker                   // the /mode dropdown, when open
	execPick    *picker                   // the /execution dropdown, when open
	sessionPick *picker                   // the /sessions and /resume dropdown
	provs       *providerManager          // the /providers screen, when open
	// wizardReturn reopens /providers when a wizard started there closes.
	wizardReturn   bool
	showKeys       bool            // the ? shortcuts overlay
	showInfo       bool            // the F2 session information panel
	usedSkills     map[string]bool // skills explicitly invoked in this TUI session
	settings       *settingsPanel  // the /config screen, when open
	reload         *reloadPrompt   // confirmation before changing the active context
	reloadApproved bool            // a confirmed execution change is being applied

	// Settings from /config.
	verbose      bool   // tool output in full
	appearance   string // compact, default, or verbose
	compactTools []agent.Event
	tips         bool   // a tip under the spinner
	mouse        bool   // wheel scrolling; off leaves text selection to the terminal
	tip          string // the tip for the running turn
	notify       string // off, bell or desktop
	focused      bool   // terminal has focus; notifications only go out without it
	// focusKnown is set once the terminal reports focus at all; until
	// then (or never, in terminals without focus events) alerts go out.
	focusKnown bool
	// The command palette shows while a bare "/name" is being typed.
	palette       *picker
	paletteQ      string // input the palette was built for
	paletteHidden string // input the palette was dismissed at with esc

	// The @ file popup and the project file index behind it.
	mention       *picker
	mentionTok    mentionToken
	mentionHidden string // token dismissed with esc
	files         []string
	filesAt       time.Time
	indexing      bool
	// MCP resources offered in the @ popup, and when they were listed.
	resources        []mcp.Resource
	resourcesAt      time.Time
	loadingResources bool
	// Prompt history: histIdx is the entry shown while browsing with
	// ↑/↓, or -1, and histDraft the input from before browsing.
	history               []string
	histIdx               int
	histDraft             string
	histPick              *picker            // ctrl+r search
	shellCancel           context.CancelFunc // a "!" command is running
	todos                 []tools.Todo       // the model's latest task list
	todoCompletionPrinted bool               // completed list already moved into conversation history
	// compactCancel is set while /compact runs. Compaction replaces the
	// context when it finishes, so nothing else may use the agent then.
	compactCancel context.CancelFunc
	recording     audio.Recording
	voiceFrame    int
	voiceReturn   int       // remaining frames before the composer reappears
	voiceStarted  time.Time // elapsed recording time
	voiceTickID   int       // invalidates ticks from an earlier recording
}

// Messages.
type (
	agentEventMsg agent.Event
	bgEventMsg    struct {
		agent.Event
		from *agent.Agent
	}
	runEndedMsg  struct{}
	outputMsg    string
	compactedMsg struct {
		summary    string
		compaction agent.CompactionInfo
		err        error
	}
)

func newModel(opts Options) *model {
	// Assume dark until the terminal answers RequestBackgroundColor; querying
	// synchronously before startup would block and swallow typed input.
	const termDark = true
	ta := textarea.New()
	ta.Placeholder = "Ask larik…  (/ commands · @ files · ! shell)"
	ta.ShowLineNumbers = false
	ta.Prompt = "› "
	ta.MaxWidth = 0 // allow the composer to follow the terminal at any width
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = 10
	ta.Focus()

	m := &model{
		opts:        opts,
		agent:       opts.Agent,
		sess:        opts.Session,
		bgStop:      make(chan struct{}),
		input:       ta,
		spin:        spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		view:        viewport.New(viewport.WithWidth(80), viewport.WithHeight(1)),
		panelView:   viewport.New(viewport.WithWidth(80), viewport.WithHeight(1)),
		width:       80,
		stats:       opts.Agent.Stats(),
		projectRoot: projectRootFor(opts.Agent.Cwd()),

		termDark: termDark,
		focused:  true,
		tips:     true,
		mouse:    true,
		notify:   "off",
		histIdx:  -1,
	}
	m.applyUIConfig()
	m.todos, _ = tools.LatestTodos(opts.History)
	m.lastReply = lastReply(opts.History)
	m.loadHistory()
	m.panelView.SoftWrap = false
	m.applyTheme(m.wantDark())
	return m
}

func (m *model) applyUIConfig() {
	var bindings map[string]config.KeyList
	if c := m.opts.Config; c != nil {
		m.appearance = c.Appearance
		if m.appearance == "" {
			if c.VerboseOn() {
				m.appearance = "verbose"
			} else {
				m.appearance = "default"
			}
		}
		m.verbose = m.appearance == "verbose"
		m.tips, m.notify, m.mouse = c.TipsOn(), c.Notifications, c.MouseOn()
		if m.notify == "" {
			m.notify = "off"
		}
		bindings = c.Keybindings
		m.vim, m.status, m.sidebarStatus = nil, nil, nil
		if c.EditorMode == "vim" {
			m.vim = newVim()
		}
		if c.StatusLine != nil {
			m.status = newStatusCmd(c.StatusLine.Command, c.StatusLine.RefreshIntervalMS)
		}
		if c.Sidebar != nil {
			m.sidebarStatus = newStatusCmd(c.Sidebar.Command, c.Sidebar.RefreshIntervalMS)
		}
	}
	m.keys, m.keyWarn = newKeymap(bindings)
	m.input.KeyMap.InsertNewline = m.keys.binding(actNewline)
	m.showThinking = m.verbose
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
	m.input.SetStyles(transparentInput(isDark))
	if m.permFeedback != nil {
		m.permFeedback.SetStyles(transparentInput(isDark))
	}
	m.setWidth(m.width)
}

// transparentInput is the textarea's default theme without the cursor-line
// background, so the composer shows the terminal's own background.
func transparentInput(isDark bool) textarea.Styles {
	st := textarea.DefaultStyles(isDark)
	st.Focused.CursorLine = lipgloss.NewStyle()
	st.Blurred.CursorLine = lipgloss.NewStyle()
	return st
}

func (m *model) setWidth(w int) {
	m.width = w
	m.view.SetWidth(max(w, 1))
	m.panelView.SetWidth(max(w, 1))
	if m.convWidth != w {
		m.rewrapConversation()
	}
	m.input.SetWidth(max(w-4, 4)) // the composer border and padding use four cells
	if m.permFeedback != nil {
		m.permFeedback.SetWidth(max(w-10, 10))
	}
	m.md, _ = glamour.NewTermRenderer(glamour.WithStyles(markdownStyle(m.isDark)), glamour.WithWordWrap(max(w-4, 20)))
}

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.RequestBackgroundColor, m.printBanner(), m.refreshProject()}
	if len(m.opts.History) > 0 {
		cmds = append(cmds, m.printHistory("resumed"))
	}
	if p := strings.TrimSpace(m.opts.InitialPrompt); p != "" {
		cmds = append(cmds, m.submit(p))
	}
	// The background listener blocks until an event arrives, so it runs
	// alongside the startup sequence rather than inside it.
	return tea.Batch(m.waitBackground(), m.statusTick(), tea.Sequence(cmds...), checkVersion(m.opts.Version))
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	md, cmd := m.update(msg)
	// Whatever changed may be something the status line shows.
	if st := m.refreshStatus(); st != nil {
		cmd = tea.Batch(cmd, st)
	}
	return md, cmd
}

func (m *model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case statusDoneMsg:
		return m, m.statusDone(msg)

	case statusTickMsg:
		return m, m.statusTick()

	case displayPreviewMsg:
		m.displayPreviewDone(msg)
		return m, nil

	case projectInfoMsg:
		m.project, m.projectLoading = msg.info, false
		return m, nil

	case versionUpdateMsg:
		if msg.version == "" {
			return m, nil
		}
		return m, m.println(m.st.warn.Render("  ! update available: " + msg.version + " (current v" + strings.TrimPrefix(m.opts.Version, "v") + ")"))

	case outputMsg:
		m.appendOutput(string(msg))
		return m, nil

	case mcpSignInURLMsg:
		return m, m.println(m.st.dim.Render("sign in to "+msg.server+" in your browser; if it didn't open, visit:\n  ") + msg.url)

	case mcpLoginDoneMsg:
		return m, m.mcpLoginDone(msg.server)

	case resourcesLoadedMsg:
		m.resources, m.resourcesAt, m.loadingResources = msg.resources, time.Now(), false
		if m.mention != nil { // rebuild the open popup with them
			m.mention = nil
			return m, m.syncMention()
		}
		return m, nil

	case filesIndexedMsg:
		m.files, m.filesAt, m.indexing = msg.files, time.Now(), false
		if m.mention != nil { // rebuild the open popup with the new list
			m.mention = nil
			return m, m.syncMention()
		}
		return m, nil

	case shellDoneMsg:
		return m, tea.Batch(m.shellDone(msg), m.refreshProject())

	case imagePastedMsg:
		return m, m.imagePasted(msg)

	case editorDoneMsg:
		if msg.err != nil {
			return m, m.println(m.st.err.Render("editor: " + msg.err.Error()))
		}
		m.input.SetValue(msg.text)
		return m, m.syncComposer()

	case tea.PasteMsg:
		if m.composerFocused() {
			if m.voiceActive() {
				return m, nil
			}
			if p, ok := m.pastedPath(msg.Content); ok {
				m.pasteMention(p)
				return m, m.syncComposer()
			}
		} else if cmd, ok := m.pasteToPanel(msg); ok {
			return m, cmd
		}

	case tea.MouseWheelMsg:
		if m.hasPickerPanel() && msg.Y >= m.panelTop && msg.Y < m.panelTop+m.panelRows {
			m.panelView, _ = m.panelView.Update(msg)
		} else if msg.Button != tea.MouseWheelLeft && msg.Button != tea.MouseWheelRight && !msg.Mod.Contains(tea.ModShift) {
			// Conversation output is already wrapped; horizontal wheel events
			// should not shift the entire message list out of view.
			m.view, _ = m.view.Update(msg)
		}
		return m, nil

	case tea.BackgroundColorMsg:
		m.termDark = msg.IsDark()
		if d := m.wantDark(); d != m.isDark {
			m.applyTheme(d)
		}
		return m, nil

	case tea.FocusMsg:
		m.focused, m.focusKnown = true, true
		return m, nil

	case tea.BlurMsg:
		m.focused, m.focusKnown = false, true
		return m, nil

	case tea.WindowSizeMsg:
		m.height = msg.Height
		m.setWidth(msg.Width)
		return m, nil

	case voiceTickMsg:
		if msg.id != m.voiceTickID || m.recording == nil && m.voiceReturn == 0 {
			return m, nil
		}
		if m.recording != nil {
			m.voiceFrame++
		} else {
			m.voiceReturn--
		}
		if m.recording != nil || m.voiceReturn > 0 {
			return m, m.voiceTick()
		}
		return m, nil

	case spinner.TickMsg:
		if !m.running && m.recording == nil && m.busyLabel == "" && m.agent.RunningBackground() == 0 {
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
		m.compactTools = nil
		m.running, m.cancel, m.events = false, nil, nil
		m.dropForegroundPerms()
		m.resetStream()
		m.stats = m.agent.Stats()
		if next := m.nextQueued(); next != nil { // pending background results ride along with it
			return m, next
		}
		return m, tea.Batch(alert, m.deliverBackground(), m.refreshProject())

	case bgEventMsg:
		if msg.from != m.agent {
			return m, nil // from a session we switched away from
		}
		e := msg.Event
		var cmds []tea.Cmd
		switch e.Kind {
		case agent.EvTaskDone:
			m.clearTaskCalls(e.Agent)
			m.dropTaskPerms(e.Agent)
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

	case recordingStartedMsg:
		m.busyLabel = ""
		if msg.err != nil {
			return m, m.println(m.st.err.Render("recording: " + msg.err.Error()))
		}
		if msg.recording == nil {
			return m, m.println(m.st.err.Render("recording: microphone did not start"))
		}
		m.recording = msg.recording
		m.voiceStarted = time.Now()
		m.voiceFrame = 0
		m.voiceReturn = 0
		m.voiceTickID++
		return m, tea.Batch(m.voiceTick(), m.spin.Tick)

	case transcriptionDoneMsg:
		m.busyLabel = ""
		m.voiceReturn = 3
		m.voiceTickID++
		var cmd tea.Cmd
		switch {
		case msg.err != nil:
			cmd = m.println(m.st.err.Render("transcription: " + msg.err.Error()))
		case strings.TrimSpace(msg.text) != "":
			m.input.SetValue(strings.TrimSpace(m.input.Value() + " " + strings.TrimSpace(msg.text)))
			cmd = m.syncComposer()
		default:
			cmd = m.println(m.st.warn.Render("transcription returned no text"))
		}
		return m, tea.Batch(cmd, m.voiceTick())

	case speechDoneMsg:
		m.busyLabel = ""
		if msg.err != nil {
			return m, m.println(m.st.err.Render("speech: " + msg.err.Error()))
		}
		return m, m.println(m.st.dim.Render("✓ Finished speaking"))

	case compactedMsg:
		if m.compactCancel != nil {
			m.compactCancel()
		}
		m.compactCancel, m.busyLabel = nil, ""
		m.stats = m.agent.Stats()
		var out tea.Cmd
		switch {
		case errors.Is(msg.err, context.Canceled):
			out = m.println(m.st.warn.Render("⏹ Compaction canceled; the conversation is unchanged"))
		case msg.err != nil:
			out = m.println(m.st.err.Render("compaction failed: " + msg.err.Error()))
		default:
			m.turnStats.Compactions++
			if msg.compaction.Available {
				m.turnStats.CompactionMeasurements++
				m.turnStats.CompactionSavedTokens += msg.compaction.SavedTokens
			}
			out = m.println(m.st.dim.Render(compactionText(msg.compaction)+"\nSummary:\n") + m.st.dim.Render(truncateLines(msg.summary, 12)))
		}
		next := m.nextQueued()
		if next == nil {
			next = m.deliverBackground()
		}
		return m, tea.Sequence(out, next)

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
		if m.hasPickerPanel() {
			switch msg.String() {
			case "ctrl+pgup":
				m.panelView.PageUp()
				return m, nil
			case "ctrl+pgdown":
				m.panelView.PageDown()
				return m, nil
			}
		}
		switch {
		case m.reload != nil:
			return m, m.handleReloadKey(msg)
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
		case m.execPick != nil:
			return m, m.handleExecPickerKey(msg)
		case m.sessionPick != nil:
			return m, m.handleSessionPickerKey(msg)
		case m.histPick != nil:
			return m, m.handleHistoryPickKey(msg)
		case m.settings != nil:
			return m, m.handleSettingsKey(msg)
		case m.provs != nil:
			return m, m.handleProvidersKey(msg)
		case m.palette != nil:
			if cmd, ok := m.handlePaletteKey(msg); ok {
				return m, cmd
			}
		case m.mention != nil:
			if cmd, ok := m.handleMentionKey(msg); ok {
				return m, cmd
			}
		}
		if m.voiceActive() && m.composerFocused() && !m.keys.is(msg.String(), actRecord) {
			return m, nil // no invisible edits or accidental submissions
		}
		switch m.keys.action[msg.String()] {
		case actScrollUp:
			m.view.PageUp()
			return m, nil
		case actScrollDown:
			m.view.PageDown()
			return m, nil
		case actScrollTop, actScrollBottom:
			// With text in the input these jump within it instead.
			if m.input.Value() == "" {
				if m.keys.is(msg.String(), actScrollTop) {
					m.view.GotoTop()
				} else {
					m.view.GotoBottom()
				}
				return m, nil
			}
		}
		md, cmd := m.handleKey(msg)
		return md, tea.Batch(cmd, m.syncComposer())
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

	if m.voiceActive() {
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, tea.Batch(cmd, m.syncComposer()) // e.g. after a paste
}

// pasteToPanel gives a paste to the open panel that has the keyboard,
// following handleKey's order. ok is false for the text fields handled
// with other messages (the wizard, routing budget and denial feedback).
// A panel with nowhere to type drops the paste rather than letting it
// land in the hidden composer.
func (m *model) pasteToPanel(msg tea.PasteMsg) (tea.Cmd, bool) {
	filter := func(p *picker) {
		p.filter += strings.Join(strings.Fields(msg.Content), " ")
		p.home()
	}
	switch {
	case m.perm != nil:
		return nil, m.permFeedback == nil
	case m.showKeys:
		return nil, true
	case m.wizard != nil, m.routing != nil:
		return nil, false
	case m.mpick != nil:
		levels := m.mpick.selectedEfforts(m)
		previous := levels[min(m.mpick.effort, len(levels)-1)]
		filter(&m.mpick.list)
		m.mpick.syncEffort(m, previous)
	case m.sessionPick != nil:
		filter(m.sessionPick)
	case m.histPick != nil:
		filter(m.histPick)
	case m.settings != nil && m.settings.rules != nil && m.settings.rules.input != nil:
		ti, cmd := m.settings.rules.input.Update(msg)
		m.settings.rules.input = &ti
		return cmd, true
	case m.settings != nil && m.settings.display != nil && m.settings.display.input != nil:
		ti, cmd := m.settings.display.input.Update(msg)
		m.settings.display.input = &ti
		return cmd, true
	case m.settings != nil && m.settings.sandbox != nil && m.settings.sandbox.input != nil:
		ti, cmd := m.settings.sandbox.input.Update(msg)
		m.settings.sandbox.input = &ti
		return cmd, true
	case m.settings != nil && m.settings.input != nil:
		ti, cmd := m.settings.input.Update(msg)
		m.settings.input = &ti
		return cmd, true
	}
	return nil, true
}

// composerFocused reports whether typing and pasting go to the composer.
func (m *model) composerFocused() bool {
	return m.perm == nil && m.wizard == nil && m.routing == nil && m.permFeedback == nil &&
		m.settings == nil && m.provs == nil && m.mpick == nil && m.modePick == nil &&
		m.execPick == nil && m.sessionPick == nil && m.histPick == nil && !m.showKeys
}

func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if k != "ctrl+c" {
		m.quitArmed = false
	}
	if k == "f2" {
		m.showInfo = !m.showInfo
		return m, nil
	}
	if k == "ctrl+c" {
		switch {
		case m.running:
			m.interrupt()
			return m, nil
		case m.shellCancel != nil:
			m.shellCancel()
			return m, nil
		case m.compactCancel != nil:
			m.compactCancel()
			return m, nil
		case m.input.Value() != "":
			m.input.Reset()
			return m, nil
		case m.quitArmed:
			return m, tea.Quit
		}
		m.quitArmed = true
		return m, nil
	}
	if m.vim != nil {
		if cmd, ok := m.vimKey(msg); ok {
			return m, cmd
		}
	}
	switch k {
	case "up":
		if m.input.Line() == 0 && m.recallHistory(-1) {
			return m, nil
		}
	case "down":
		if m.histIdx >= 0 && m.input.Line() == m.input.LineCount()-1 && m.recallHistory(1) {
			return m, nil
		}
	}
	switch m.keys.action[k] {
	case actQuit:
		if m.input.Value() == "" {
			return m, tea.Quit
		}
	case actInterrupt:
		if m.running {
			m.interrupt()
			return m, nil
		}
		if m.shellCancel != nil {
			m.shellCancel()
			return m, nil
		}
		if m.compactCancel != nil {
			m.compactCancel()
			return m, nil
		}
	case actHistorySearch:
		m.openHistoryPicker()
		return m, nil
	case actExternalEditor:
		return m, m.openEditor()
	case actPasteImage:
		return m, m.pasteImage()
	case actRecord:
		return m, m.toggleRecording()
	case actCycleMode:
		return m, m.cycleMode()
	case actToggleThinking:
		return m, m.toggleThinking()
	case actShortcuts:
		if m.input.Value() == "" {
			m.showKeys = true
			return m, nil
		}
	case actModelPicker:
		if m.running {
			return m, m.println(m.st.err.Render("the model can't change while a turn is running" + m.interruptHint()))
		}
		return m, m.openModelPicker()
	case actSubmit:
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
		}
		m.input.Reset()
		if m.vim != nil {
			m.vim.reset()
		}
		m.recordHistory(text)
		if strings.HasPrefix(text, "/") {
			return m, m.command(text)
		}
		if strings.HasPrefix(text, "!") {
			if !m.idle() {
				return m, m.println(m.st.err.Render("wait for the current turn or command to finish" + m.interruptHint()))
			}
			return m, m.runShell(text[1:])
		}
		if !m.idle() {
			m.queue = append(m.queue, text)
			return m, nil
		}
		return m, m.submit(text)
	}
	before := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.histIdx >= 0 && m.input.Value() != before { // editing detaches from history
		m.histIdx, m.histDraft = -1, ""
	}
	return m, cmd
}

// idle reports whether the agent is free: no turn, "!" command or
// compaction is running.
func (m *model) idle() bool {
	return !m.running && m.shellCancel == nil && m.compactCancel == nil
}

// nextQueued submits the oldest queued prompt when the agent is idle.
func (m *model) nextQueued() tea.Cmd {
	if len(m.queue) == 0 || !m.idle() {
		return nil
	}
	next := m.queue[0]
	m.queue = m.queue[1:]
	return m.submit(next)
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
	m.running, m.cancel, m.prompted = true, cancel, true
	m.turnStart, m.turnChars = time.Now(), 0
	m.turnStats = turnStats{}
	m.tip = nextTip(m.keys)
	m.events = m.agent.Run(ctx, text)
	return tea.Sequence(
		m.println("\n"+m.renderUserMessage(text)),
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
		return m.println(m.st.dim.Render("thinking collapsed" + paren(m.keyHint(actToggleThinking, "to show"))))
	}
	if !m.running && m.lastThinking != "" {
		return m.println(m.st.thinking.Render("✻ " + truncateLines(wrap(m.lastThinking, m.width-4), 40)))
	}
	return m.println(m.st.dim.Render("thinking shown" + paren(m.keyHint(actToggleThinking, "to collapse"))))
}

func (m *model) handleEvent(e agent.Event) tea.Cmd {
	// Remember which model a subagent runs on, so the live rows and the
	// sidebar can show which tier is doing the work.
	if e.Agent != "" && e.Model != "" {
		if m.taskModels == nil {
			m.taskModels = make(map[string]string)
		}
		m.taskModels[e.Agent] = e.Model
	}
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
		if e.Agent == "" {
			if t := strings.TrimSpace(e.Message.Text()); t != "" {
				m.lastReply = t
			}
		}
		m.stream.Reset()
		m.thinking.Reset()
		m.calling = ""
		m.thinkStart, m.thinkDur = time.Time{}, 0
		if out == "" {
			return nil
		}
		shown := m.println(out)
		if e.Agent == "" && m.opts.Config != nil && m.opts.Config.Audio.AutoSpeak {
			return tea.Batch(shown, m.speak(m.lastReply))
		}
		return shown
	case agent.EvToolStart:
		m.tools = append(m.tools, toolRun{id: e.ToolID, name: e.ToolName, input: e.Input, agent: e.Agent, started: time.Now()})
	case agent.EvToolEnd:
		for i, t := range m.tools {
			if t.id == e.ToolID && t.agent == e.Agent {
				if !t.started.IsZero() {
					m.turnStats.ToolTime += time.Since(t.started)
				}
				m.tools = append(m.tools[:i], m.tools[i+1:]...)
				break
			}
		}
		if m.updateTodos(e) {
			return m.println(m.completedTodosCard())
		}
		// The card names the model the task ran on, so it is rendered before
		// that is forgotten.
		card := m.renderToolCard(e)
		if m.appearance == "compact" && !e.IsError && e.ToolName != permission.ExitPlanTool {
			m.compactTools = append(m.compactTools, e)
		}
		if e.Agent != "" {
			if m.taskCalls == nil {
				m.taskCalls = make(map[string]int)
			}
			m.taskCalls[e.Agent]++
		} else if e.ToolName == "task" && !taskIsBackground(e.Input) {
			m.clearTaskCalls(taskLabel(e.Input))
		}

		if m.appearance != "compact" || e.IsError || e.ToolName == permission.ExitPlanTool {
			return m.println(card)
		}
		return nil
	case agent.EvPermission:
		// A plan goes into the conversation, where it can be scrolled and
		// stays for reference; the prompt only asks about it.
		var plan tea.Cmd
		if e.ToolName == permission.ExitPlanTool {
			plan = m.println(m.renderPlan(e.Input))
		}
		if m.perm != nil {
			m.permQueue = append(m.permQueue, e)
			return plan
		}
		ev := e
		m.perm, m.permIdx = &ev, 0
		what := "needs your permission to use " + e.ToolName
		if e.ToolName == permission.ExitPlanTool {
			what = "has a plan for you to review"
		}
		return tea.Sequence(plan, m.alert(what))
	case agent.EvUsage:
		m.stats = *e.Usage
		m.turnStats.Steps++
		m.turnStats.ModelTime += time.Duration(e.Usage.RequestMS) * time.Millisecond
		if e.Usage.TTFTMS > 0 {
			m.turnStats.TTFTTotal += time.Duration(e.Usage.TTFTMS) * time.Millisecond
			m.turnStats.TTFTCount++
		}
	case agent.EvCompacted:
		text := "✓ Context compacted to stay within the model's window."
		if e.Compaction != nil {
			text = compactionText(*e.Compaction)
		}
		if e.Agent != "" {
			// A subagent summarized its own context, not this one. Its spend
			// is already counted as delegated usage, and the turn and session
			// compression figures describe the main context, so they stay out
			// of it; it is shown because it says the child is doing enough
			// work to have filled a context.
			return m.println(m.agentLine(e, m.st.dim.Render(text)))
		}
		m.stats = m.agent.Stats()
		if e.Compaction != nil {
			m.turnStats.Compactions++
			if e.Compaction.Available {
				m.turnStats.CompactionMeasurements++
				m.turnStats.CompactionSavedTokens += e.Compaction.SavedTokens
			}
		}
		return m.println(m.st.dim.Render(text))
	case agent.EvNotice:
		if strings.HasPrefix(e.Text, "running /") && m.opts.Skills != nil {
			name := strings.TrimPrefix(e.Text, "running /")
			name, _, _ = strings.Cut(name, " ")
			if _, ok := m.opts.Skills.Get(name); ok {
				if m.usedSkills == nil {
					m.usedSkills = make(map[string]bool)
				}
				m.usedSkills[name] = true
			}
		}
		return m.println(m.agentLine(e, m.st.warn.Render("! "+e.Text)))
	case agent.EvError:
		return m.println(m.agentLine(e, m.st.err.Render("✗ "+e.Text)))
	case agent.EvDone:
		if e.StopReason != "interrupted" && m.appearance == "compact" && len(m.compactTools) > 0 {
			return m.println(m.compactToolSummary())
		}
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
		reply = &m.permAllows()[0]
	case "2", "a":
		reply = &m.permAllows()[1]
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
		reply = &m.permAllows()[m.permIdx]
	}
	if reply == nil {
		return nil
	}
	return m.replyPermission(*reply)
}

// permAllows are the replies of the prompt's first two options: allow once
// and always for a tool; for a plan, approve and accept edits or approve
// and keep asking.
func (m *model) permAllows() []agent.PermissionReply {
	if m.perm != nil && m.perm.ToolName == permission.ExitPlanTool {
		return []agent.PermissionReply{{Allow: true, Mode: permission.ModeAcceptEdits}, {Allow: true, Mode: permission.ModeDefault}}
	}
	return []agent.PermissionReply{{Allow: true}, {Allow: true, Always: true}}
}

func (m *model) startPermissionFeedback() {
	ta := textarea.New()
	ta.Placeholder = "What should larik do instead?"
	if m.perm != nil && m.perm.ToolName == permission.ExitPlanTool {
		ta.Placeholder = "What should change in the plan?"
	}
	ta.ShowLineNumbers = false
	ta.Prompt = "› "
	ta.DynamicHeight = true
	ta.MinHeight, ta.MaxHeight = 1, 4
	ta.SetWidth(max(m.width-10, 10))
	ta.SetStyles(transparentInput(m.isDark))
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
	if ev.ToolName == permission.ExitPlanTool {
		if !reply.Allow {
			return m.println(m.st.dim.Render("  ⎿ kept planning"))
		}
		mode := reply.Mode
		if mode == "" {
			mode = permission.ModeDefault
		}
		return m.println(m.st.dim.Render("  ⎿ plan approved · continuing in ") + m.st.modeStyle(mode).UnsetPadding().Render(modeLabels[mode]))
	}
	if !reply.Allow {
		return m.println(m.st.dim.Render("  ⎿ denied " + toolTitle(ev.ToolName, ev.Input, m.shortPaths)))
	}
	if reply.Always {
		return m.println(m.st.dim.Render("  ⎿ won't ask again for " + ev.SuggestedRule + " this session"))
	}
	return nil
}

func (m *model) println(s string) tea.Cmd {
	return func() tea.Msg { return outputMsg(s) }
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
	if !m.idle() || m.agent.PendingNotifications() == 0 {
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
	m.turnStats = turnStats{}
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

// dropTaskPerms closes the prompts of a background task that has ended;
// nothing is waiting for their answers any more.
func (m *model) dropTaskPerms(label string) {
	ended := func(e *agent.Event) bool {
		return e != nil && m.bgReplies[e.Reply] && taskMatches(label, e.Agent)
	}
	var queue []agent.Event
	for i := range m.permQueue {
		if ended(&m.permQueue[i]) {
			delete(m.bgReplies, m.permQueue[i].Reply)
		} else {
			queue = append(queue, m.permQueue[i])
		}
	}
	if ended(m.perm) {
		delete(m.bgReplies, m.perm.Reply)
		m.perm, m.permFeedback = nil, nil
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
