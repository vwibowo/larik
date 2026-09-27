package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

type shortcutGroup struct {
	title string
	keys  [][2]string
}

var shortcutGroups = []shortcutGroup{
	{"typing", [][2]string{
		{"enter", "send"},
		{"shift+enter", "new line (also alt+enter, ctrl+j)"},
		{"ctrl+c", "clear the input"},
		{"ctrl+d", "quit when the input is empty"},
	}},
	{"during a turn", [][2]string{
		{"esc", "interrupt"},
		{"enter", "queue a follow-up"},
		{"ctrl+o", "show or hide thinking"},
	}},
	{"model and mode", [][2]string{
		{"alt+p", "switch model and effort"},
		{"shift+tab", "cycle default → accept edits → plan"},
		{"/mode", "all modes, including yolo"},
	}},
	{"find things", [][2]string{
		{"/", "command palette"},
		{"?", "these shortcuts"},
		{"/providers", "manage providers"},
		{"/sessions", "sessions in this directory"},
	}},
	{"permission prompt", [][2]string{
		{"y  a  n", "yes · always · no"},
		{"1 2 3  ↑/↓", "select"},
		{"esc", "deny"},
	}},
	{"leave", [][2]string{
		{"ctrl+c ×2", "quit"},
	}},
}

// shortcutsView is the ? overlay: every key in one place, in two columns
// when the terminal is wide enough.
func (m *model) shortcutsView() string {
	w := max(m.width-6, 20)
	render := func(groups []shortcutGroup, width int) string {
		var lines []string
		for i, g := range groups {
			if i > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, m.st.dim.Render(strings.ToUpper(g.title)))
			for _, kv := range g.keys {
				lines = append(lines, pad(m.st.user.Render(kv[0]), 14)+m.st.dim.Render(kv[1]))
			}
		}
		return lipgloss.NewStyle().Width(width).Render(strings.Join(lines, "\n"))
	}
	var body string
	if w >= 90 {
		half := (len(shortcutGroups) + 1) / 2
		col := (w - 4) / 2
		body = lipgloss.JoinHorizontal(lipgloss.Top, render(shortcutGroups[:half], col), "    ", render(shortcutGroups[half:], col))
	} else {
		body = render(shortcutGroups, w)
	}
	head := spread(m.st.accent.Render("Keyboard shortcuts"), m.st.dim.Render("any key to close"), w)
	return m.st.modal.Width(max(m.width-2, 10)).Render(head + "\n\n" + body)
}
