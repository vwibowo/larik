package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/mcp"
	"larik/internal/permission"
)

func (m *model) View() tea.View {
	var parts []string

	if m.running || m.busyLabel != "" {
		if live := m.liveView(); live != "" {
			parts = append(parts, live)
		}
	}

	if m.perm != nil {
		parts = append(parts, m.permissionView())
	} else {
		parts = append(parts, m.st.box.Width(max(m.width-2, 10)).Render(m.input.View()))
	}
	parts = append(parts, m.statusLine())

	v := tea.NewView(strings.Join(parts, "\n"))
	v.WindowTitle = "larik"
	return v
}

// liveView shows the in-flight response, clipped to the screen.
func (m *model) liveView() string {
	var b []string
	if t := strings.TrimSpace(m.thinking.String()); t != "" && m.stream.Len() == 0 {
		b = append(b, m.st.thinking.Render("✻ "+lastLines(wrap(t, m.width-4), 3)))
	}
	if s := m.stream.String(); s != "" {
		limit := max(m.height-12, 5)
		b = append(b, lastLines(wrap(s, m.width-2), limit))
	}
	for _, t := range m.tools {
		line := m.st.accent.Render(m.spin.View()+" ") + toolTitle(t.name, t.input)
		if t.agent != "" {
			line = "  ↳ " + line + m.st.dim.Render("  ("+t.agent+")")
		}
		b = append(b, line)
	}
	label := ""
	switch {
	case m.busyLabel != "":
		label = m.busyLabel
	case m.calling != "":
		label = "writing " + m.calling + " call…"
	case len(m.tools) == 0 && m.stream.Len() == 0:
		label = "thinking…"
	}
	if label != "" {
		b = append(b, m.st.accent.Render(m.spin.View())+" "+m.st.dim.Render(label))
	}
	if len(m.queue) > 0 {
		b = append(b, m.st.dim.Render(fmt.Sprintf("⧗ %d message(s) queued", len(m.queue))))
	}
	return strings.Join(b, "\n")
}

func (m *model) permissionView() string {
	e := m.perm
	var body strings.Builder
	title := "Allow this " + e.ToolName + " call?"
	if e.Agent != "" {
		title += m.st.dim.Render("  requested by subagent " + e.Agent)
	}
	if n := len(m.permQueue); n > 0 {
		title += m.st.dim.Render(fmt.Sprintf("  (+%d waiting)", n))
	}
	body.WriteString(m.st.accent.Render(title) + "\n\n")
	body.WriteString(m.permDetail(e) + "\n\n")
	opts := []string{"Yes", "Yes, and always allow " + e.SuggestedRule, "No, tell the model to do something else"}
	for i, o := range opts {
		cursor := "  "
		line := fmt.Sprintf("%d. %s", i+1, o)
		if i == m.permIdx {
			cursor = m.st.accent.Render("› ")
			line = m.st.accent.Render(line)
		}
		body.WriteString(cursor + line + "\n")
	}
	body.WriteString(m.st.dim.Render("\n↑/↓ select · enter confirm · y/a/n · esc deny"))
	return m.st.modal.Width(max(m.width-2, 10)).Render(body.String())
}

func (m *model) permDetail(e *agent.Event) string {
	var in map[string]any
	_ = json.Unmarshal(e.Input, &in)
	switch e.ToolName {
	case "bash":
		cmd := lipgloss.NewStyle().Bold(true).Render("$ " + str(in["command"]))
		if sb, ok := in["sandbox"].(bool); ok && !sb && m.agent.Perms().Sandboxed() {
			cmd += "\n" + m.st.warn.Render("runs OUTSIDE the sandbox: full file system and network access")
		}
		return cmd
	case "write":
		content := strings.TrimSuffix(str(in["content"]), "\n")
		return str(in["path"]) + m.st.dim.Render(fmt.Sprintf("  (%d lines)", strings.Count(content, "\n")+1)) + "\n" +
			m.st.diffAdd.Render(truncateLines(prefixLines(content, "+ "), 12))
	case "web_fetch":
		return lipgloss.NewStyle().Bold(true).Render(str(in["url"])) + "\n" + m.st.dim.Render("fetches over the network from Larik (outside the sandbox)")
	case "web_search":
		return lipgloss.NewStyle().Bold(true).Render(str(in["query"])) + "\n" + m.st.dim.Render("the query is sent to the configured search provider")
	case "edit":
		return str(in["path"]) + "\n" + m.diff(prefixLines(str(in["old_string"]), "- ")+"\n"+prefixLines(str(in["new_string"]), "+ "), 16)
	}
	return string(e.Input)
}

func (m *model) statusLine() string {
	mode := m.agent.Perms().Mode()
	modeLabel := map[permission.Mode]string{
		permission.ModeDefault:     "default",
		permission.ModeAcceptEdits: "⏵⏵ accept edits",
		permission.ModePlan:        "⏸ plan mode",
		permission.ModeYolo:        "⚠ yolo",
	}[mode]
	left := m.st.statusMode.Render(modeLabel) + m.st.dim.Render(" (shift+tab)")

	var right []string
	right = append(right, m.agent.ProviderName()+"/"+m.agent.Model())
	if e := m.agent.Effort(); e != "" {
		right = append(right, "effort "+string(e))
	}
	if m.stats.ContextWindow > 0 && m.stats.ContextTokens > 0 {
		right = append(right, fmt.Sprintf("ctx %d%%", m.stats.ContextTokens*100/m.stats.ContextWindow))
	}
	if m.stats.CostUSD > 0 {
		right = append(right, fmt.Sprintf("$%.2f", m.stats.CostUSD))
	}
	if n := m.agent.RunningBackground(); n > 0 {
		right = append(right, fmt.Sprintf("⧗ %d background", n))
	}
	if m.running {
		right = append(right, "esc to interrupt")
	} else if m.quitArmed {
		right = append(right, "ctrl+c again to quit")
	}
	r := m.st.dim.Render(strings.Join(right, " · "))
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(r)
	if gap < 1 {
		return left + "\n" + r
	}
	return left + strings.Repeat(" ", gap) + r
}

// renderAssistant formats a finished assistant message for scrollback.
func (m *model) renderAssistant(msg llm.Message) string {
	var out []string
	for _, b := range msg.Blocks {
		switch b.Type {
		case llm.BlockThinking:
			if t := strings.TrimSpace(b.Text); t != "" {
				out = append(out, m.st.thinking.Render("✻ "+truncateLines(wrap(t, m.width-4), 6)))
			}
		case llm.BlockText:
			if t := strings.TrimSpace(b.Text); t != "" {
				out = append(out, m.renderMarkdown(t))
			}
		}
	}
	return strings.Join(out, "\n")
}

func (m *model) renderMarkdown(s string) string {
	if m.md == nil {
		return s
	}
	r, err := m.md.Render(s)
	if err != nil {
		return s
	}
	return strings.Trim(r, "\n")
}

func (m *model) renderToolCard(e agent.Event) string {
	bullet := m.st.ok.Render("●")
	if e.IsError {
		bullet = m.st.err.Render("●")
	}
	head := bullet + " " + lipgloss.NewStyle().Bold(true).Render(toolTitle(e.ToolName, e.Input))
	var body string
	switch {
	case e.Agent != "" && !e.IsError:
		// keep nested subagent activity to one line
	case e.Display != "":
		body = m.diff(e.Display, 20)
	case e.IsError:
		body = m.st.err.Render(truncateLines(strings.TrimSpace(e.Output), 6))
	default:
		out := strings.TrimSpace(e.Output)
		n := strings.Count(out, "\n") + 1
		switch e.ToolName {
		case "read":
			body = m.st.dim.Render(fmt.Sprintf("read %d lines", n))
		case "skill":
			body = m.st.dim.Render(fmt.Sprintf("loaded skill (%d lines)", n))
		case "web_fetch":
			body = m.st.dim.Render(e.Display)
			if strings.Contains(out, "redirected (HTTP") {
				body = m.st.dim.Render(truncateLines(out, 2))
			}
		case "web_search":
			var titles []string
			for _, l := range strings.Split(out, "\n") {
				if len(l) > 2 && l[0] >= '1' && l[0] <= '9' && strings.Contains(l[:4], ". ") {
					titles = append(titles, l)
				}
			}
			body = m.st.dim.Render(truncateLines(strings.Join(titles, "\n"), 4))
		case "grep", "glob":
			body = m.st.dim.Render(truncateLines(out, 4))
		default:
			body = m.st.dim.Render(truncateLines(out, 6))
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

// toolTitle is a one-line summary like `bash(go test ./...)`.
func toolTitle(name string, input []byte) string {
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
	case "web_fetch":
		arg = str(in["url"])
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
	if len(arg) > 80 {
		arg = arg[:77] + "…"
	}
	return fmt.Sprintf("%s(%s)", name, arg)
}

func (m *model) printBanner() tea.Cmd {
	cwd := m.opts.Config.Cwd
	if home := homeDir(); home != "" && strings.HasPrefix(cwd, home) {
		cwd = "~" + strings.TrimPrefix(cwd, home)
	}
	lines := []string{
		m.st.accent.Render("larik") + m.st.dim.Render(" v"+m.opts.Version),
		m.st.dim.Render(m.agent.ProviderName() + "/" + m.agent.Model() + " · " + cwd),
		m.st.dim.Render("enter send · shift+enter newline · esc interrupt · /help"),
	}
	switch {
	case m.opts.Sandbox != nil:
		lines = append(lines, m.st.dim.Render("sandbox: "+m.opts.Sandbox.Kind()+" · bash runs confined to the project, no network · /sandbox"))
	case m.opts.SandboxNote != "":
		lines = append(lines, m.st.warn.Render("no sandbox: "+m.opts.SandboxNote))
	}
	if m.opts.SearchNote != "" {
		lines = append(lines, m.st.warn.Render("web_search disabled: "+m.opts.SearchNote))
	}
	if !m.opts.Config.ProjectHooksApproved() {
		lines = append(lines, m.st.warn.Render("project hooks in .larik/settings.json are not approved yet · review with /hooks"))
	}
	if id := m.agent.SessionID(); id != "" {
		lines = append(lines, m.st.dim.Render("session "+id+" · resume with larik --resume "+id))
	}
	return m.println(m.st.box.Render(strings.Join(lines, "\n")))
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
			if out := m.renderAssistant(msg); out != "" {
				b = append(b, out)
			}
			for _, u := range msg.ToolUses() {
				b = append(b, m.st.dim.Render("● "+toolTitle(u.Name, u.Input)))
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
