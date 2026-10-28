package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"larik/internal/agent"
	"larik/internal/permission"
)

// asking reports whether the open prompt is an ask_user question rather
// than a permission request. It shares the permission prompt's queue and
// reply channel; only its options and answers differ.
func (m *model) asking() bool {
	return m.perm != nil && m.perm.ToolName == permission.AskUserTool
}

// handleAskKey answers a question: a number or enter picks an option, the
// last option opens a text field for the user's own words, esc dismisses.
func (m *model) handleAskKey(msg tea.KeyPressMsg) tea.Cmd {
	opts := agent.OptionsOf(m.perm.Input)
	n := len(opts) + 1 // the options, then "type something else"
	pick := func(i int) tea.Cmd {
		if i == len(opts) {
			m.startPermissionFeedback()
			return nil
		}
		return m.replyPermission(agent.PermissionReply{Allow: true, Answer: opts[i].Label})
	}
	switch k := msg.String(); k {
	case "up", "k", "shift+tab":
		m.permIdx = (m.permIdx + n - 1) % n
	case "down", "j", "tab":
		m.permIdx = (m.permIdx + 1) % n
	case "enter":
		return pick(m.permIdx)
	case "esc":
		return m.replyPermission(agent.PermissionReply{Allow: false})
	case "ctrl+c":
		m.interrupt()
		return m.replyPermission(agent.PermissionReply{Allow: false})
	default:
		if len(k) == 1 && k[0] >= '1' && int(k[0]-'0') <= n {
			return pick(int(k[0] - '1'))
		}
	}
	return nil
}

// handleAskFeedbackKey handles the text field of a question: enter sends
// the typed answer, esc goes back to the options.
func (m *model) handleAskFeedbackKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "enter":
		answer := strings.TrimSpace(m.permFeedback.Value())
		if answer == "" {
			return nil, true
		}
		return m.replyPermission(agent.PermissionReply{Allow: true, Answer: answer}), true
	case "esc":
		m.permFeedback = nil
		return nil, true
	}
	return nil, false
}

// askView shows a question, its options and the keys that answer it.
func (m *model) askView() string {
	e := m.perm
	w := max(m.width-6, 20)
	var meta []string
	if n := len(m.permQueue); n > 0 {
		meta = append(meta, fmt.Sprintf("%d more waiting", n))
	}
	lines := []string{
		spread(m.st.accent.Render("Larik has a question"), m.st.dim.Render(strings.Join(meta, " · ")), w),
		m.st.warn.Render(m.permissionWaitLabel()),
		"",
		prefixLines(wrap(agent.QuestionOf(e.Input), w-2), "  "),
		"",
	}
	if m.permFeedback != nil {
		lines = append(lines, "Your answer:", m.permFeedback.View(), "", m.st.dim.Render("enter send · esc back to the options · ctrl+c dismiss and stop"))
		return m.st.modal.Width(max(m.width-2, 10)).Render(strings.Join(lines, "\n"))
	}
	opts := agent.OptionsOf(e.Input)
	opts = append(opts, agent.AskOption{Label: "Type something else"})
	for i, o := range opts {
		line := fmt.Sprintf("%d. %s", i+1, o.Label)
		cursor := "  "
		if i == m.permIdx {
			cursor = m.st.accent.Render("› ")
			line = m.st.accent.Render(line)
		}
		lines = append(lines, spread(cursor+line, m.st.dim.Render(fmt.Sprint(i+1)), w))
		if o.Description != "" && i == m.permIdx {
			lines = append(lines, m.st.dim.Render(prefixLines(wrap(o.Description, w-5), "     ")))
		}
	}
	lines = append(lines, "", m.st.dim.Render("↑/↓ select · enter confirm · esc dismiss · ctrl+c dismiss and stop"))
	return m.st.modal.Width(max(m.width-2, 10)).Render(strings.Join(lines, "\n"))
}
