package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/mcp"
	"larik/internal/permission"
	"larik/internal/tools"
)

func (m *model) View() tea.View {
	height := m.height
	if height <= 0 {
		height = 24 // before the first size event
	}
	modal := m.perm != nil || m.showKeys // these take the composer's place
	bottom := m.statusLine()
	if !modal {
		bottom = m.composerView() + "\n" + bottom
	}
	m.frameBottomRows = lipgloss.Height(bottom)
	defer func() { m.frameBottomRows = 0 }()
	room := max(height-m.frameBottomRows, 0)

	showSidebar := m.showInfo && m.width >= 100 && !m.showKeys
	sidebarWidth := 0
	if showSidebar {
		sidebarWidth = min(max(m.width/3, 30), 44)
	}
	conversationWidth := max(m.width-sidebarWidth, 1)
	if m.convWidth != conversationWidth {
		m.rewrapConversationWidth(conversationWidth)
	}
	m.view.SetWidth(conversationWidth)

	panelKind, panelText := m.panel()
	if m.panelKind != panelKind {
		m.panelKind = panelKind
		m.panelView.GotoTop()
	}
	panelRows := 0
	if panelText != "" {
		lines := strings.Split(panelText, "\n")
		panelRows = min(len(lines), panelRoom(room))
		switch {
		case panelRows == len(lines) || panelRows == 0:
		case modal:
			// Keep the answer options, which come last, in view.
			panelText = strings.Join(lines[len(lines)-panelRows:], "\n")
		case panelRows >= 3:
			// Keep the modal border in place while its content scrolls.
			m.panelView.SetContent(strings.Join(lines[1:len(lines)-1], "\n"))
			m.panelView.SetHeight(panelRows - 2)
			panelText = lines[0] + "\n" + m.panelView.View() + "\n" + lines[len(lines)-1]
		default:
			m.panelView.SetContent(panelText)
			m.panelView.SetHeight(panelRows)
			panelText = m.panelView.View()
		}
	}

	// The live view sits under the conversation rather than inside it, so a
	// spinner tick doesn't re-layout the whole conversation.
	var live []string
	if m.running || m.busyLabel != "" || m.agent.RunningBackground() > 0 {
		if s := m.liveView(); s != "" {
			live = strings.Split(s, "\n")
		}
	}
	liveRoom := room - panelRows
	if liveRoom >= 8 {
		liveRoom -= 3 // keep some conversation in view
	}
	liveRows := min(len(live), max(liveRoom, 0))
	live = live[len(live)-liveRows:]

	viewRows := max(room-panelRows-liveRows, 0)
	atBottom := m.view.AtBottom()
	m.view.SetHeight(viewRows)
	if atBottom {
		m.view.GotoBottom()
	}
	var rows []string
	if viewRows > 0 {
		conversation := m.view.View()
		if showSidebar {
			left := strings.Split(conversation, "\n")
			right := strings.Split(m.sessionSidebar(sidebarWidth, viewRows), "\n")
			for i := range left {
				l := ansi.Truncate(left[i], conversationWidth, " ")
				r := ""
				if i < len(right) {
					r = right[i]
				}
				rows = append(rows, l+" "+r)
			}
		} else {
			if m.openTodos() && m.width >= 64 {
				conversation = m.floatTodos(conversation, conversationWidth)
			}
			rows = append(rows, conversation)
		}
	}
	rows = append(rows, live...)
	m.panelTop, m.panelRows = viewRows+liveRows, panelRows
	if panelRows > 0 {
		rows = append(rows, panelText)
	}
	rows = append(rows, bottom)

	v := tea.NewView(strings.Join(rows, "\n"))
	v.AltScreen = true
	if m.mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	v.WindowTitle = "larik"
	v.ReportFocus = m.notify != "off"
	return v
}

// panel renders the open panel, if any, and names it so its scroll
// position resets when a different one opens.
func (m *model) panel() (kind, text string) {
	switch {
	case m.perm != nil:
		return "permission", m.permissionView()
	case m.showKeys:
		return "shortcuts", m.shortcutsView()
	case m.showInfo && m.width < 100:
		return "session-info", m.sessionInfoView()
	case m.provs != nil:
		kind = "providers"
		if it, ok := m.provs.list.selected(); ok {
			kind += fmt.Sprintf(":%v", it.value)
		}
		return kind, m.providersView()
	case m.settings != nil:
		return "settings:" + m.settings.editing, m.settingsView()
	case m.wizard != nil:
		return fmt.Sprintf("wizard:%d", m.wizard.step), m.st.modal.Width(max(m.width-2, 10)).Render(m.wizard.view(m.st, max(m.width-6, 20), m.availablePanelRows()-2))
	case m.routing != nil:
		return fmt.Sprintf("routing:%d", m.routing.step), m.st.modal.Width(max(m.width-2, 10)).Render(m.routing.view(m.st, max(m.width-6, 20), m.availablePanelRows()-2))
	case m.mpick != nil:
		return "model", m.modelPickerView()
	case m.modePick != nil:
		return "mode", m.modePickerView()
	case m.sessionPick != nil:
		return "sessions", m.sessionPickerView()
	case m.histPick != nil:
		return "history", m.historyPickerView()
	case m.palette != nil:
		return "palette", m.paletteView()
	case m.mention != nil:
		return "mention", m.mentionView()
	}
	return "", ""
}

// sessionSections renders the session panel as rows exactly width columns
// wide: what is active now, split into sections. Details that the footer or
// banner already show (model, mode, sandbox kind) are left out.
func (m *model) sessionSections(width, taskLimit int) [][]string {
	fit := func(s string) string { return ansi.Truncate(s, width, "…") }
	head := func(title, summary string) string {
		return fit(spread(m.st.accent.Render(title), m.st.dim.Render(summary), width))
	}
	row := func(left, right string) string {
		left = ansi.Truncate(left, max(width-lipgloss.Width(right)-1, 1), "…")
		return fit(spread(left, right, width))
	}
	var sections [][]string

	if m.opts.Sandbox == nil {
		note := "! sandbox off"
		if m.opts.SandboxNote != "" {
			note = "! no sandbox: " + m.opts.SandboxNote
		}
		sections = append(sections, []string{m.st.warn.Render(fit(note))})
	}

	if m.openTodos() {
		sec := []string{head("Tasks", todoProgress(m.todos))}
		for _, l := range m.todoLinesWidth(m.todos, taskLimit, max(width-2, 4)) {
			sec = append(sec, fit(l))
		}
		sections = append(sections, sec)
	}

	var mcpRows []string
	if m.opts.MCP != nil {
		statuses := m.opts.MCP.Statuses()
		sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
		for _, s := range statuses {
			switch s.State {
			case mcp.StateConnected:
				mcpRows = append(mcpRows, row(m.st.ok.Render("● ")+s.Name, m.st.dim.Render(plural(len(s.Tools), "tool"))))
			case mcp.StateConnecting:
				mcpRows = append(mcpRows, row(m.st.dim.Render("◐ ")+s.Name, m.st.dim.Render("connecting")))
			case mcp.StateNeedsApproval:
				mcpRows = append(mcpRows, row(m.st.warn.Render("! ")+s.Name, m.st.warn.Render("needs approval")))
			case mcp.StateFailed:
				mcpRows = append(mcpRows, row(m.st.err.Render("✕ ")+s.Name, m.st.err.Render("failed")))
			}
		}
	}
	summary := "none"
	if len(mcpRows) > 0 {
		summary = fmt.Sprintf("%d active", len(mcpRows))
	}
	sections = append(sections, append([]string{head("MCP", summary)}, mcpRows...))

	var lspRows []string
	if m.opts.LSP != nil {
		statuses := m.opts.LSP.Statuses()
		sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
		for _, s := range statuses {
			switch {
			case len(s.Failed) > 0:
				lspRows = append(lspRows, row(m.st.err.Render("✕ ")+s.Name, m.st.err.Render("failed")))
			case len(s.Running) > 0:
				lspRows = append(lspRows, row(m.st.ok.Render("● ")+s.Name, m.st.dim.Render("running")))
			}
		}
	}
	summary = "none active"
	if len(lspRows) > 0 {
		summary = fmt.Sprintf("%d active", len(lspRows))
	}
	sections = append(sections, append([]string{head("LSP", summary)}, lspRows...))

	var used []string
	for name := range m.usedSkills {
		used = append(used, name)
	}
	sort.Strings(used)
	sec := []string{head("Skills", fmt.Sprintf("%d used · %d", len(used), len(m.opts.Skills.List())))}
	for _, name := range used {
		sec = append(sec, fit(m.st.ok.Render("✓ ")+"/"+name))
	}
	if len(used) == 0 {
		sec = append(sec, m.st.dim.Render(fit("none used yet · / to browse")))
	}
	return append(sections, sec)
}

// joinSections puts a rule between sections.
func (m *model) joinSections(sections [][]string, width int) []string {
	var out []string
	for i, sec := range sections {
		if i > 0 {
			out = append(out, m.st.dim.Render(strings.Repeat("─", width)))
		}
		out = append(out, sec...)
	}
	return out
}

func (m *model) sessionInfoView() string {
	inner := max(m.width-6, 10)
	rows := []string{m.st.accent.Render("Session") + m.st.dim.Render("  ·  F2 or any key to close"), ""}
	rows = append(rows, m.joinSections(m.sessionSections(inner, 0), inner)...)
	return m.st.modal.Width(max(m.width-2, 10)).Render(strings.Join(rows, "\n"))
}

// sessionSidebar renders the session panel as a bordered column width
// columns wide and height rows tall.
func (m *model) sessionSidebar(width, height int) string {
	inner := max(width-5, 8) // border, padding, and the gap before the panel
	room := max(height-2, 1) // inside the border
	hint := m.st.dim.Render(fitRight("F2 to hide", inner))
	taskLimit := max(height/3, 3)
	rows := m.joinSections(m.sessionSections(inner, taskLimit), inner)
	if len(rows)+1 > room && m.openTodos() {
		// Shrink the task list before cutting the rest.
		taskLimit = max(taskLimit-(len(rows)+1-room), 2)
		rows = m.joinSections(m.sessionSections(inner, taskLimit), inner)
	}
	if len(rows)+1 > room {
		keep := max(room-2, 0)
		more := m.st.dim.Render(fmt.Sprintf("+%d more", len(rows)-keep))
		rows = append(rows[:keep], more)
	}
	for len(rows)+1 < room {
		rows = append(rows, "")
	}
	rows = append(rows, hint)
	return m.st.modal.Width(inner + 4).Render(strings.Join(rows[:min(len(rows), room)], "\n"))
}

// fitRight right-aligns s in width columns.
func fitRight(s string, width int) string {
	return strings.Repeat(" ", max(width-lipgloss.Width(s), 0)) + s
}

// floatTodos overlays a compact checklist card over the top-right of the
// conversation viewport. The card is bounded so it never consumes the view.
func (m *model) floatTodos(conversation string, width int) string {
	cardWidth := min(max(width/3, 26), 44)
	cardWidth = min(cardWidth, width-4)
	if cardWidth < 12 {
		return conversation
	}
	limit := min(max(m.height/4, 3), 7)
	content := append([]string{m.st.accent.Render("Tasks · " + todoProgress(m.todos))}, m.todoLinesWidth(m.todos, limit, cardWidth-4)...)
	innerWidth := max(cardWidth-4, 1)
	card := m.st.modal.Width(innerWidth).Render(strings.Join(content, "\n"))
	baseLines := strings.Split(conversation, "\n")
	cardLines := strings.Split(card, "\n")
	cardRows := min(len(cardLines), len(baseLines))
	start := max(width-lipgloss.Width(card), 0)
	for i := 0; i < cardRows; i++ {
		left := ansi.Cut(baseLines[i], 0, start)
		right := ansi.Cut(baseLines[i], start+lipgloss.Width(card), width)
		baseLines[i] = left + cardLines[i] + right
	}
	return strings.Join(baseLines, "\n")
}

func (m *model) composerView() string {
	box := m.st.box
	if m.shellMode() {
		box = box.BorderForeground(m.st.warn.GetForeground())
	}
	return box.Width(max(m.width, 7)).Render(m.input.View())
}

// availablePanelRows is the height a panel may use above the composer and
// status line, leaving a few conversation rows visible.
func (m *model) availablePanelRows() int {
	height := m.height
	if height <= 0 {
		height = 24 // a few tests render a panel before the first size event
	}
	bottom := m.frameBottomRows
	if bottom == 0 {
		bottom = lipgloss.Height(m.composerView()) + lipgloss.Height(m.statusLine())
	}
	return panelRoom(height - bottom)
}

func panelRoom(room int) int {
	switch {
	case room <= 1:
		return 0
	case room < 8:
		return room - 1
	}
	return room - 3
}

func (m *model) hasPickerPanel() bool {
	return m.provs != nil || m.settings != nil || m.wizard != nil || m.routing != nil ||
		m.mpick != nil || m.modePick != nil || m.sessionPick != nil || m.palette != nil ||
		m.histPick != nil || m.mention != nil
}

// liveView shows the in-flight response, clipped to the screen, and a
// status line with what the model is doing and for how long.
func (m *model) liveView() string {
	var b []string
	thinkingNow := m.thinking.Len() > 0 && m.stream.Len() == 0 && m.calling == ""
	if thinkingNow && m.showThinking {
		b = append(b, m.st.thinking.Render("✻ "+lastLines(wrap(strings.TrimSpace(m.thinking.String()), m.width-4), 8)))
	}
	if s := m.stream.String(); s != "" {
		limit := max(m.height-12, 5)
		b = append(b, lastLines(wrap(s, m.width-2), limit))
	}
	if m.running && m.width < 64 {
		b = append(b, m.liveTodos()...)
	}
	tasks := m.taskRows()
	b = append(b, tasks...)
	for _, t := range m.tools {
		if len(tasks) > 0 && (t.name == "task" || t.agent != "") {
			continue // task activity is grouped by subagent above
		}
		line := m.st.accent.Render(m.spin.View()+" ") + toolTitle(t.name, t.input, m.shortPaths)
		if t.agent != "" {
			line = "  ↳ " + line + m.st.dim.Render("  ("+t.agent+")")
		}
		b = append(b, line)
	}

	var label string
	switch {
	case m.perm != nil:
		label = "Waiting for your answer…"
	case m.busyLabel != "":
		label = m.busyLabel
	case !m.running && len(tasks) > 0:
		label = "Background tasks running…"
	case m.calling != "":
		label = "Writing " + m.calling + " call…"
	case thinkingNow:
		label = "Thinking…"
	case len(m.tools) > 0:
		label = "Working…"
	case m.stream.Len() > 0:
		label = "Responding…"
	default:
		label = "Waiting for the model…"
	}
	// Name the task in progress rather than what the loop is doing, for
	// the generic states.
	if a := m.activeTodo(); a != "" && m.running && m.perm == nil && m.busyLabel == "" && m.calling == "" && !thinkingNow {
		label = a + "…"
	}
	var meta []string
	if m.running {
		meta = append(meta, elapsed(time.Since(m.turnStart)))
		if m.turnChars > 0 {
			meta = append(meta, fmt.Sprintf("↓ ~%d tokens", m.turnChars/4)) // a rough count while streaming
		}
		if thinkingNow && !m.showThinking {
			meta = appendHint(meta, m.keyHint(actToggleThinking, "to show thinking"))
		}
		if m.perm == nil { // with a prompt open, esc denies instead
			meta = appendHint(meta, m.keyHint(actInterrupt, "to interrupt"))
		}
	}
	line := m.st.accent.Render("✻ ") + label
	if len(meta) > 0 {
		line += m.st.dim.Render("  " + strings.Join(meta, " · "))
	}
	b = append(b, line)
	if m.tips && m.tip != "" && m.running && m.height >= 15 {
		b = append(b, m.st.dim.Render("  Tip: "+m.tip))
	}
	if len(m.queue) > 0 {
		b = append(b, m.st.dim.Render(fmt.Sprintf("⧗ %d message(s) queued, sent when this turn ends", len(m.queue))))
	}
	return strings.Join(b, "\n")
}

// taskLabel matches the label used when the task tool forwards child events.
func taskLabel(input []byte) string {
	var in struct {
		Type        string `json:"subagent_type"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(input, &in)
	if in.Description != "" {
		return in.Type + ": " + strings.TrimSpace(in.Description)
	}
	return in.Type
}

func taskIsBackground(input []byte) bool {
	var in struct {
		Background bool `json:"run_in_background"`
	}
	_ = json.Unmarshal(input, &in)
	return in.Background
}

func taskMatches(label, child string) bool {
	return child == label || strings.HasPrefix(child, label+" · ")
}

func (m *model) clearTaskCalls(label string) {
	for name := range m.taskCalls {
		if taskMatches(label, name) {
			delete(m.taskCalls, name)
		}
	}
}

// taskRows gives each parallel subagent its own live row. A tool-call count
// reflects observed work; it is not a percentage of an unknown total.
func (m *model) taskRows() []string {
	type task struct {
		label, id string
		started   time.Time
	}
	var running []task
	background := m.agent.BackgroundTasks()
	for _, t := range m.tools {
		if t.name != "task" {
			continue
		}
		label := taskLabel(t.input)
		if taskIsBackground(t.input) {
			found := false
			for _, bg := range background {
				found = found || bg.Label == label && bg.Status == agent.BgRunning
			}
			if found {
				continue
			}
		}
		running = append(running, task{label: label, started: t.started})
	}
	for _, bg := range background {
		if bg.Status == agent.BgRunning {
			running = append(running, task{label: bg.Label, id: bg.ID, started: bg.Started})
		}
	}
	if len(running) == 0 {
		return nil
	}
	limit := min(len(running), max(m.height/3, 3))
	rows := []string{m.st.dim.Render(fmt.Sprintf("Subagents (%d running)", len(running)))}
	for _, task := range running[:limit] {
		calls, activity := 0, "thinking…"
		for name, n := range m.taskCalls {
			if taskMatches(task.label, name) {
				calls += n
			}
		}
		for _, t := range m.tools {
			if t.agent != "" && taskMatches(task.label, t.agent) {
				activity = toolTitle(t.name, t.input, m.shortPaths)
			}
		}
		waiting := m.perm != nil && taskMatches(task.label, m.perm.Agent)
		if waiting {
			activity = "waiting for permission"
		}
		name := task.label
		if task.id != "" {
			name = task.id + " " + name
		}
		age := "0s"
		if !task.started.IsZero() {
			age = elapsed(time.Since(task.started))
		}
		line := fmt.Sprintf("  ↳ %s · %s · %s · %s", name, age, plural(calls, "tool"), activity)
		style := m.st.dim
		if waiting {
			style = m.st.warn
		}
		rows = append(rows, style.Render(ansi.Truncate(line, max(m.width-2, 10), "…")))
	}
	if len(running) > limit {
		rows = append(rows, m.st.dim.Render(fmt.Sprintf("  … +%d subagents", len(running)-limit)))
	}
	return rows
}

// lines is how many lines of tool output a card shows: n, or up to
// verboseLines with verbose output on.
func (m *model) lines(n int) int {
	if m.verbose {
		return verboseLines
	}
	return n
}

const verboseLines = 40

// plural formats a count with its noun: "1 line", "3 lines".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// elapsed formats a duration as "14s" or "2m 05s".
func elapsed(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// permissionView asks whether a tool call may run: a question naming the
// action, where the request came from, what exactly will happen, and the
// three answers with their keys.
func (m *model) permissionView() string {
	e := m.perm
	w := max(m.width-6, 20)
	meta := []string{permToolName(e)}
	if e.Agent != "" {
		meta = append(meta, "from subagent "+e.Agent)
	}
	if n := len(m.permQueue); n > 0 {
		meta = append(meta, fmt.Sprintf("%d more waiting", n))
	}
	lines := []string{
		spread(m.st.accent.Render(m.permQuestion(e)), m.st.dim.Render(strings.Join(meta, " · ")), w),
		"",
		prefixLines(m.permDetail(e), "  "),
		"",
	}
	if e.AutoReason != "" {
		// Auto mode didn't approve this one; say why it's being asked.
		lines = append(lines, m.st.warn.Render(wrap("✦ auto mode asks: "+e.AutoReason, w)), "")
	}
	if m.permFeedback != nil {
		lines = append(lines, "Tell larik what to do instead:", m.permFeedback.View(), "", m.st.dim.Render("enter send denial · esc deny without feedback · ctrl+c deny and stop"))
		return m.st.modal.Width(max(m.width-2, 10)).Render(strings.Join(lines, "\n"))
	}
	opts := []struct{ text, rule, key, note string }{
		{text: "Yes", key: "y"},
		{text: "Yes, and don't ask again for ", rule: e.SuggestedRule, key: "a", note: "save to private project settings"},
		{text: "No, tell larik what to do instead", key: "n"},
	}
	if e.ToolName == permission.ExitPlanTool {
		opts = []struct{ text, rule, key, note string }{
			{text: "Yes, and accept edits", key: "y", note: "edit project files without asking; commands still ask"},
			{text: "Yes, but ask before each edit", key: "2"},
			{text: "No, keep planning", key: "n", note: "tell larik what to change"},
		}
	}
	for i, o := range opts {
		line := fmt.Sprintf("%d. %s", i+1, o.text)
		cursor := "  "
		if i == m.permIdx {
			cursor = m.st.accent.Render("› ")
			line = m.st.accent.Render(line + o.rule)
		} else if o.rule != "" {
			line += m.st.accent.Render(o.rule)
		}
		lines = append(lines, spread(cursor+line, m.st.dim.Render(o.key), w))
		if o.note != "" && i == m.permIdx {
			lines = append(lines, m.st.dim.Render("     "+o.note))
		}
	}
	hint := "↑/↓ select · enter confirm · esc deny · ctrl+c deny and stop"
	if e.ToolName == permission.ExitPlanTool {
		hint = "↑/↓ select · enter confirm · esc keep planning · ctrl+c stop"
	}
	lines = append(lines, "", m.st.dim.Render(hint))
	return m.st.modal.Width(max(m.width-2, 10)).Render(strings.Join(lines, "\n"))
}

// permQuestion names what the call will do.
func (m *model) permQuestion(e *agent.Event) string {
	var in map[string]any
	_ = json.Unmarshal(e.Input, &in)
	switch e.ToolName {
	case "bash":
		return "Run this command?"
	case "write":
		path := str(in["path"])
		if !filepath.IsAbs(path) {
			base := e.Cwd // a worktree subagent's own directory
			if base == "" && m.opts.Config != nil {
				base = m.opts.Config.Cwd
			}
			path = filepath.Join(base, path)
		}
		if _, err := os.Stat(path); err == nil {
			return "Overwrite this file?"
		}
		return "Create this file?"
	case "edit":
		return "Make this edit?"
	case tools.MultiEditToolName:
		return "Make these edits?"
	case "apply_code_action":
		return "Apply this code action?"
	case "web_fetch":
		return "Fetch this page?"
	case "web_search":
		return "Search the web?"
	case "browser_navigate":
		return "Open this page in the browser?"
	case "browser_click":
		return "Click this in the browser?"
	case "browser_type":
		return "Type this in the browser?"
	case "browser_eval":
		return "Run this script in the page?"
	case "browser_upload":
		return "Upload these files to the page?"
	case permission.ExitPlanTool:
		return "Ready to start on this plan?"
	}
	if server := mcp.ServerOf(e.ToolName); server != "" {
		return "Use " + permToolName(e) + "?"
	}
	return "Allow " + e.ToolName + "?"
}

// permToolName is the tool as people know it: "bash (unsandboxed)",
// "github › create_issue".
func permToolName(e *agent.Event) string {
	if e.ToolName == permission.ExitPlanTool {
		return "leaves plan mode"
	}
	if server := mcp.ServerOf(e.ToolName); server != "" {
		return server + " › " + strings.TrimPrefix(e.ToolName, mcp.Prefix+server+"__")
	}
	if e.ToolName == "bash" {
		var in map[string]any
		_ = json.Unmarshal(e.Input, &in)
		if sb, ok := in["sandbox"].(bool); ok && !sb {
			return "bash (unsandboxed)"
		}
	}
	return e.ToolName
}

func (m *model) permDetail(e *agent.Event) string {
	var in map[string]any
	_ = json.Unmarshal(e.Input, &in)
	bold := lipgloss.NewStyle().Bold(true)
	switch e.ToolName {
	case "bash":
		cmd := bold.Render("$ " + str(in["command"]))
		if sb, ok := in["sandbox"].(bool); ok && !sb && m.agent.Perms().Sandboxed() {
			cmd += "\n" + m.st.warn.Render("! runs outside the sandbox: full file system and network access")
		}
		return cmd
	case "write":
		content := strings.TrimSuffix(str(in["content"]), "\n")
		return bold.Render(m.shortPaths(str(in["path"]))) + m.st.dim.Render("  "+plural(strings.Count(content, "\n")+1, "line")) + "\n" +
			m.highlightDiff(str(in["path"]), prefixLines(content, "+ "), 12)
	case "web_fetch":
		return bold.Render(str(in["url"])) + "\n" + m.st.dim.Render("fetched by larik over the network, outside the sandbox")
	case "web_search":
		return bold.Render(str(in["query"])) + "\n" + m.st.dim.Render("the query goes to the configured search provider")
	case "browser_navigate":
		return bold.Render(str(in["url"])) + "\n" + m.st.dim.Render("opens in larik's Chrome window, with its saved sign-ins")
	case "browser_type":
		return bold.Render(str(in["text"])) + "\n" + m.st.dim.Render("into "+str(in["ref"])+" on the current page")
	case "browser_eval":
		return bold.Render(str(in["expression"]))
	case "browser_upload":
		paths, _ := in["paths"].([]any)
		var names []string
		for _, p := range paths {
			names = append(names, m.shortPaths(str(p)))
		}
		return bold.Render(strings.Join(names, "\n")) + "\n" + m.st.dim.Render("sent to the website through "+str(in["ref"])+" on the current page")
	case "apply_code_action":
		line, _ := in["line"].(float64)
		return bold.Render(str(in["title"])) + "\n" + m.st.dim.Render(fmt.Sprintf("from the language server, at %s:%d; it may change other files too", m.shortPaths(str(in["path"])), int(line)))
	case permission.ExitPlanTool:
		plan := agent.PlanOf(e.Input)
		return m.st.dim.Render(fmt.Sprintf("The plan (%s) is shown above; scroll up to read it all.", plural(strings.Count(plan, "\n")+1, "line")))
	case "edit":
		return bold.Render(m.shortPaths(str(in["path"]))) + "\n" + m.highlightDiff(str(in["path"]), prefixLines(str(in["old_string"]), "- ")+"\n"+prefixLines(str(in["new_string"]), "+ "), 16)
	case tools.MultiEditToolName:
		edits, _ := in["edits"].([]any)
		var parts []string
		for _, e := range edits {
			ed, _ := e.(map[string]any)
			parts = append(parts, prefixLines(str(ed["old_string"]), "- ")+"\n"+prefixLines(str(ed["new_string"]), "+ "))
		}
		return bold.Render(m.shortPaths(str(in["path"]))) + m.st.dim.Render("  "+plural(len(edits), "edit")) + "\n" +
			m.highlightDiff(str(in["path"]), strings.Join(parts, "\n  ⋯\n"), 24)
	}
	if len(in) == 0 {
		return m.st.dim.Render("no arguments")
	}
	pretty, _ := json.MarshalIndent(in, "", "  ")
	return m.st.dim.Render(truncateLines(string(pretty), 12))
}

// statusLine is the footer: the permission mode on the left; model,
// effort, context use and cost on the right. Hints drop out first when
// the terminal is narrow.
func (m *model) statusLine() string {
	mode := m.agent.Perms().Mode()
	modeChip := m.st.modeStyle(mode).Render(modeLabels[mode]) + m.vimTag()
	if m.sess != nil && m.sess.Trace() != nil {
		modeChip += " " + m.st.err.Render("● rec")
	}
	if m.status != nil && len(m.status.lines) > 0 && !m.quitArmed {
		return m.customStatus(modeChip)
	}

	model := m.st.accent.Render("◆ ") + m.st.user.Render(m.agent.Model())
	provider := m.st.dim.Render(" " + m.agent.ProviderName())
	var extra []string
	if e := m.agent.Effort(); e != "" {
		style, mark := m.st.effortStyle(e)
		extra = append(extra, m.st.dim.Render("effort ")+style.Render(string(e)+" "+mark))
	}
	// Sandbox state stays in the footer: it is the one safety setting that
	// used to hide in the sidebar.
	sandbox := m.st.ok.Render("◈ sandbox")
	if m.opts.Sandbox == nil {
		sandbox = m.st.warn.Bold(true).Render("⚠ no sandbox")
		if m.agent.Perms().Mode() == permission.ModeYolo {
			sandbox = m.st.err.Bold(true).Render("⚠ no sandbox")
		}
	}
	ctxPct, ctxBar := "", "" // "31%", "▰▰▰▱▱▱▱▱▱▱ "
	if m.stats.ContextWindow > 0 && m.stats.ContextTokens > 0 {
		pct := min(m.stats.ContextTokens*100/m.stats.ContextWindow, 100)
		style := m.st.accent
		switch {
		case pct >= 90:
			style = m.st.err
		case pct >= 70:
			style = m.st.warn
		}
		const cells = 10
		full := (pct*cells + 50) / 100
		ctxBar = style.Render(strings.Repeat("▰", full)) + m.st.dim.Render(strings.Repeat("▱", cells-full)) + " "
		ctxPct = style.Render(fmt.Sprintf("%d%%", pct))
	}
	var tail []string
	if m.stats.CostUSD > 0 {
		tail = append(tail, m.costText())
	}
	if n := m.agent.RunningBackground(); n > 0 {
		tail = append(tail, fmt.Sprintf("⧗ %d background", n))
	}
	if m.quitArmed {
		tail = append(tail, m.st.warn.Render("ctrl+c again to quit"))
	}

	sep := m.st.dim.Render(" · ")
	build := func(hint, withProvider, withBar, withSandbox bool) string {
		left := modeChip
		if k := m.keys.hint(actCycleMode); hint && k != "" {
			left += m.st.dim.Render(" " + k)
		}
		parts := []string{model}
		if withProvider {
			parts[0] += provider
		}
		if withSandbox {
			parts = append(parts, sandbox)
		}
		parts = append(parts, extra...)
		if ctxPct != "" {
			ctx := m.st.dim.Render("ctx ") + ctxPct
			if withBar {
				ctx = m.st.dim.Render("ctx ") + ctxBar + ctxPct
			}
			parts = append(parts, ctx)
		}
		parts = append(parts, tail...)
		right := strings.Join(parts, sep)
		gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
		if gap < 2 {
			return ""
		}
		return left + strings.Repeat(" ", gap) + right
	}
	for _, v := range [][4]bool{{true, true, true, true}, {false, true, true, true}, {false, true, false, true}, {false, false, false, true}, {false, false, false, false}} {
		if line := build(v[0], v[1], v[2], v[3]); line != "" {
			return line
		}
	}
	return modeChip + "\n" + model + provider
}

// costText is the session cost. With a session budget it shows the cap and
// turns amber at the warn threshold and red at the cap.
func (m *model) costText() string {
	spent := m.stats.CostUSD
	text := fmt.Sprintf("$%.2f", spent)
	c := m.opts.Config
	if c == nil {
		return text
	}
	b := c.Routing().Budget
	if b.SessionUSD <= 0 {
		return text
	}
	text += fmt.Sprintf("/$%.2f", b.SessionUSD)
	switch {
	case spent >= b.SessionUSD:
		return m.st.err.Render(text)
	case spent >= b.WarnFraction()*b.SessionUSD:
		return m.st.warn.Render(text)
	}
	return text
}

// renderAssistant formats a finished assistant message for scrollback.
// thought is how long the model thought, if known. Thinking is collapsed
// to one line unless ctrl+o turned it on.
func (m *model) renderAssistant(msg llm.Message, thought time.Duration) string {
	var out []string
	for _, b := range msg.Blocks {
		switch b.Type {
		case llm.BlockThinking:
			if b.DurationMS > 0 { // measured by the agent and saved with the session
				thought = time.Duration(b.DurationMS) * time.Millisecond
			}
			t := strings.TrimSpace(b.Text)
			switch {
			case t == "":
			case m.showThinking:
				out = append(out, m.st.thinking.Render("✻ "+truncateLines(wrap(t, m.width-4), 20)))
			case thought >= time.Second:
				out = append(out, m.st.thinking.Render("✻ Thought for "+elapsed(thought))+m.thinkingHint())
			default:
				out = append(out, m.st.thinking.Render("✻ Thought")+m.thinkingHint())
			}
		case llm.BlockText:
			if t := strings.TrimSpace(b.Text); t != "" {
				out = append(out, m.renderMarkdown(t))
			}
		}
	}
	return strings.Join(out, "\n")
}

// renderMarkdown renders headings, code blocks and prose apart and joins
// them without glamour's blank lines; only blank lines between prose
// paragraphs remain.
func (m *model) renderMarkdown(s string) string {
	if m.md == nil {
		return s
	}
	var b strings.Builder
	for i, seg := range splitMarkdown(s) {
		r, err := m.md.Render(seg.text)
		if err != nil {
			return s
		}
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(trimBlankLines(r))
	}
	return b.String()
}

func (m *model) renderToolCard(e agent.Event) string {
	bullet := m.st.ok.Render("●")
	if e.IsError {
		bullet = m.st.err.Render("●")
	}
	head := bullet + " " + lipgloss.NewStyle().Bold(true).Render(toolTitle(e.ToolName, e.Input, m.shortPaths))
	e.Output, e.Display = m.shortPaths(e.Output), m.shortPaths(e.Display)
	var body string
	switch {
	case e.Agent != "" && !e.IsError:
		// keep nested subagent activity to one line
	case e.IsError:
		body = m.st.err.Render(truncateLines(strings.TrimSpace(e.Output), m.lines(6)))
	case e.ToolName == "read":
		out := strings.TrimSpace(e.Output)
		n := strings.Count(out, "\n") + 1
		body = m.st.dim.Render("read " + plural(n, "line"))
		if m.verbose {
			preview := strings.Split(out, "\n")
			for i := range preview {
				preview[i] = ansi.Truncate(preview[i], max(min(m.width-12, 120), 12), "…")
			}
			body += "\n" + m.st.dim.Render(truncateLines(strings.Join(preview, "\n"), 3))
		}
	case e.ToolName == tools.TodoToolName:
		// Keep each update as a compact activity entry. The live task card
		// shows progress; only the final completed list enters the transcript.
	case e.ToolName == permission.ExitPlanTool:
		// the plan itself was printed when it was put to the user
	case e.ToolName == "write":
		var in struct{ Path, Content string }
		_ = json.Unmarshal(e.Input, &in)
		if in.Content == "" {
			body = m.st.dim.Render("(empty file)")
		} else {
			body = m.highlightDiff(in.Path, prefixLines(strings.TrimSuffix(in.Content, "\n"), "+ "), m.lines(12))
		}
	case (e.ToolName == "edit" || e.ToolName == tools.MultiEditToolName) && e.Display != "":
		var in struct{ Path string }
		_ = json.Unmarshal(e.Input, &in)
		body = m.highlightDiff(in.Path, e.Display, m.lines(20))
	case e.Display != "":
		body = m.diff(e.Display, m.lines(20))
	default:
		out := strings.TrimSpace(e.Output)
		n := strings.Count(out, "\n") + 1
		switch e.ToolName {
		case "skill":
			body = m.st.dim.Render("loaded skill (" + plural(n, "line") + ")")
		case "web_fetch":
			body = m.st.dim.Render(e.Display)
			if strings.Contains(out, "redirected (HTTP") {
				body = m.st.dim.Render(truncateLines(out, m.lines(2)))
			}
		case "web_search":
			var titles []string
			for _, l := range strings.Split(out, "\n") {
				if len(l) > 2 && l[0] >= '1' && l[0] <= '9' && strings.Contains(l[:4], ". ") {
					titles = append(titles, l)
				}
			}
			body = m.st.dim.Render(truncateLines(strings.Join(titles, "\n"), m.lines(4)))
		case "grep", "glob":
			body = m.st.dim.Render(truncateLines(out, m.lines(4)))
		default:
			body = m.st.dim.Render(truncateLines(out, m.lines(6)))
		}
	}
	if diag := diagnosticLines(e.Output); diag != "" && e.ToolName != "lsp" {
		if body != "" {
			body += "\n"
		}
		body += m.st.warn.Render(diag)
	}
	card := head
	if body != "" {
		card += "\n" + prefixLines(body, "  ⎿ ", "    ")
	}
	if e.Agent != "" { // nest subagent activity under its task
		card = prefixLines(card, "  ↳ ", "    ") + m.st.dim.Render("  ("+e.Agent+")")
	}
	return card
}

func (m *model) diff(s string, maxLines int) string {
	lines := strings.Split(truncateLines(s, maxLines), "\n")
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "+"):
			lines[i] = m.st.diffAdd.Render(l)
		case strings.HasPrefix(l, "-"):
			lines[i] = m.st.diffDel.Render(l)
		default:
			lines[i] = m.st.dim.Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

// toolTitle is a one-line summary like `bash(go test ./...)`. shorten,
// if set, rewrites paths in the argument for display.
func toolTitle(name string, input []byte, shorten func(string) string) string {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	arg := ""
	switch name {
	case "bash":
		arg = str(in["command"])
		if sb, ok := in["sandbox"].(bool); ok && !sb {
			name = "bash (unsandboxed)"
		}
	case "read", "write", "edit":
		arg = str(in["path"])
	case "apply_code_action":
		name = "lsp › apply"
		arg = str(in["title"])
	case tools.MultiEditToolName:
		arg = str(in["path"])
		if edits, ok := in["edits"].([]any); ok {
			arg += ", " + plural(len(edits), "edit")
		}
	case "grep":
		arg = str(in["pattern"])
		if p := str(in["path"]); p != "" {
			arg += " in " + p
		}
	case "glob":
		arg = str(in["pattern"])
	case "skill":
		arg = str(in["name"])
	case "lsp":
		name = "lsp › " + str(in["operation"])
		arg = str(in["path"])
		if l, ok := in["line"].(float64); ok {
			arg += fmt.Sprintf(":%d", int(l))
			if c, ok := in["column"].(float64); ok {
				arg += fmt.Sprintf(":%d", int(c))
			}
		}
		if q := str(in["query"]); q != "" {
			arg = q
		}
	case "task":
		name = "task › " + str(in["subagent_type"])
		arg = str(in["description"])
		if bg, _ := in["run_in_background"].(bool); bg {
			arg += " ⧗"
		}
	case "task_wait":
		if ids, ok := in["ids"].([]any); ok {
			for i, id := range ids {
				if i > 0 {
					arg += ", "
				}
				arg += fmt.Sprint(id)
			}
		} else {
			arg = "all"
		}
	case "task_stop":
		arg = str(in["id"])
	case permission.ExitPlanTool:
		name = "plan"
		arg = firstLine(agent.PlanOf(input))
	case tools.TodoToolName:
		name = "todos"
		if todos, err := tools.ParseTodos(input); err == nil {
			arg = todoProgress(todos)
		}
	case "web_fetch", "browser_navigate":
		arg = str(in["url"])
	case "browser_click", "browser_select":
		arg = str(in["ref"])
	case "browser_type":
		arg = str(in["ref"]) + " " + strconv.Quote(str(in["text"]))
	case "browser_press_key":
		arg = str(in["key"])
	case "browser_eval":
		arg = str(in["expression"])
	case "browser_history":
		arg = str(in["direction"])
	case "browser_upload":
		paths, _ := in["paths"].([]any)
		arg = str(in["ref"]) + " " + plural(len(paths), "file")
	case "browser_wait_for":
		switch {
		case str(in["text"]) != "":
			arg = strconv.Quote(str(in["text"]))
		case str(in["text_gone"]) != "":
			arg = "until " + strconv.Quote(str(in["text_gone"])) + " is gone"
		default:
			secs, _ := in["seconds"].(float64)
			arg = fmt.Sprintf("%gs", secs)
		}
	case "browser_screenshot":
		arg = "viewport"
		if full, _ := in["full_page"].(bool); full {
			arg = "full page"
		}
		if ref := str(in["ref"]); ref != "" {
			arg = ref
		}
		if labels, _ := in["labels"].(bool); labels {
			arg += " with labels"
		}
	case "browser_tabs":
		arg = str(in["action"])
	case "web_search":
		arg = str(in["query"])
		if s := str(in["site"]); s != "" {
			arg += " site:" + s
		}
	default:
		if server := mcp.ServerOf(name); server != "" {
			name = server + " › " + strings.TrimPrefix(name, mcp.Prefix+server+"__")
		}
		if len(in) > 0 {
			arg = string(input)
		}
	}
	arg = strings.Join(strings.Fields(arg), " ")
	if shorten != nil {
		arg = shorten(arg)
	}
	arg = oneLine(arg, 80)
	return fmt.Sprintf("%s(%s)", name, arg)
}

// renderPlan shows a plan put to the user for approval.
func (m *model) renderPlan(input json.RawMessage) string {
	plan := agent.PlanOf(input)
	if plan == "" {
		plan = "(no plan given)"
	}
	head := m.st.accent.Render("◆ Plan")
	return "\n" + head + "\n" + m.renderMarkdown(plan)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimLeft(line, "# ")
}

// printBanner prints two short lines, plus warnings only when they apply.
func (m *model) printBanner() tea.Cmd {
	head := m.st.accent.Render("✻ larik") + m.st.dim.Render(" v"+m.opts.Version+" · "+shortPath(m.opts.Config.Cwd))
	if m.opts.Sandbox != nil {
		head += m.st.dim.Render(" · " + m.opts.Sandbox.Kind() + " sandbox")
	}
	lines := []string{
		head,
		m.st.dim.Render("  " + strings.Join(appendHint(appendHint([]string{"/ commands", "? shortcuts"},
			m.keyHint(actModelPicker, "model")), m.keyHint(actCycleMode, "mode")), " · ")),
	}
	for _, w := range m.keyWarn {
		lines = append(lines, m.st.warn.Render("  ! "+w))
	}
	warn := func(s string) { lines = append(lines, m.st.warn.Render("  ! "+s)) }
	if m.opts.Sandbox == nil && m.opts.SandboxNote != "" {
		warn("no sandbox: " + m.opts.SandboxNote)
	}
	if m.sess != nil && m.sess.TraceErr != nil {
		warn("debug trace: " + m.sess.TraceErr.Error())
	} else if m.sess != nil && m.sess.Trace() != nil {
		warn("debug mode: recording prompts, requests and tool output to " + shortPath(m.sess.Trace().Dir()) + " · /trace to review")
	}
	if m.opts.SearchNote != "" {
		warn("web_search disabled: " + m.opts.SearchNote)
	}
	if !m.opts.Config.ProjectHooksApproved() {
		warn("project hooks in .larik/settings.json are not approved yet · review with /hooks")
	}
	return m.println(strings.Join(lines, "\n"))
}

// shortPaths rewrites absolute paths in text for display: inside the
// project they become relative ("./" for the project itself), elsewhere
// under the home directory they start with ~. What the model sees is
// unchanged.
func (m *model) shortPaths(s string) string {
	if m.opts.Config != nil {
		if cwd := strings.TrimRight(m.opts.Config.Cwd, "/"); cwd != "" {
			s = replacePathPrefix(s, cwd, ".")
		}
	}
	if home := homeDir(); home != "" {
		s = replacePathPrefix(s, home, "~")
	}
	return s
}

// replacePathPrefix replaces dir where it starts a path: "dir/x" becomes
// "x" (or "~/x" when with is "~"), and dir on its own becomes with. It
// leaves dir alone inside a longer name, such as "dir-old/x".
func replacePathPrefix(s, dir, with string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, dir)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		rest := s[i+len(dir):]
		startsPath := i == 0 || !isPathByte(s[i-1])
		switch {
		case startsPath && strings.HasPrefix(rest, "/"):
			b.WriteString(s[:i])
			if with == "~" {
				b.WriteString("~/")
			}
			s = rest[1:]
		case startsPath && (rest == "" || !isPathByte(rest[0])):
			b.WriteString(s[:i] + with)
			s = rest
		default:
			b.WriteString(s[:i+len(dir)])
			s = rest
		}
	}
}

func isPathByte(c byte) bool {
	return c == '/' || c == '.' || c == '-' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// shortPath abbreviates the home directory to ~ and keeps long paths to
// their last two directories.
func shortPath(p string) string {
	p = tildePath(p)
	if len(p) <= 50 {
		return p
	}
	parts := strings.Split(p, "/")
	if len(parts) <= 3 {
		return p
	}
	return "…/" + strings.Join(parts[len(parts)-2:], "/")
}

func (m *model) printHistory(label string) tea.Cmd {
	var b []string
	for _, msg := range m.opts.History {
		switch msg.Role {
		case llm.RoleUser:
			if t := strings.TrimSpace(msg.Text()); t != "" {
				b = append(b, "\n"+m.st.user.Render("› "+indentAfterFirst(t, "  ")))
			}
		case llm.RoleAssistant:
			if out := m.renderAssistant(msg, 0); out != "" {
				b = append(b, out)
			}
			for _, u := range msg.ToolUses() {
				b = append(b, m.st.dim.Render("● "+toolTitle(u.Name, u.Input, m.shortPaths)))
			}
		}
	}
	b = append(b, m.st.dim.Render("── "+label+" ──"))
	return m.println(strings.Join(b, "\n"))
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func wrap(s string, width int) string {
	if width < 10 {
		width = 10
	}
	return lipgloss.Wrap(s, width, " ")
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func truncateLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + fmt.Sprintf("\n… +%d lines", len(lines)-n)
}

// prefixLines prefixes the first line with first and the rest with rest
// (rest defaults to first).
func prefixLines(s, first string, rest ...string) string {
	other := first
	if len(rest) > 0 {
		other = rest[0]
	}
	lines := strings.Split(s, "\n")
	for i := range lines {
		if i == 0 {
			lines[i] = first + lines[i]
		} else {
			lines[i] = other + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

func indentAfterFirst(s, indent string) string {
	return strings.ReplaceAll(s, "\n", "\n"+indent)
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

// diagnosticLines extracts LSP feedback appended to edit/write results.
func diagnosticLines(out string) string {
	if !strings.Contains(out, "<diagnostics") {
		return ""
	}
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "ERROR ") || strings.HasPrefix(l, "WARN ") || strings.HasPrefix(l, "This change introduced") {
			lines = append(lines, "⚠ "+l)
		}
	}
	return truncateLines(strings.Join(lines, "\n"), 5)
}
