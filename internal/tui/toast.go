package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// A toast is a short confirmation drawn over the top-right corner of the
// screen for a moment, instead of a line left in the conversation.

const toastFor = 2 * time.Second

type toastMsg string

type toastExpiredMsg struct{ id int }

// showToast puts text in the corner until toastFor passes or another
// toast replaces it.
func (m *model) showToast(text string) tea.Cmd {
	m.toast = text
	m.toastID++
	id := m.toastID
	return tea.Tick(toastFor, func(time.Time) tea.Msg { return toastExpiredMsg{id} })
}

// overlayToast draws the open toast over screen, a full frame width
// columns wide.
func (m *model) overlayToast(screen string, width int) string {
	if m.toast == "" {
		return screen
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForegroundBlend(m.st.accentColor, m.st.sparkColor).Padding(0, 1).
		Render(m.st.ok.Render("✓ ") + m.toast)
	x := width - lipgloss.Width(box) - 1
	if x < 0 {
		return screen
	}
	return lipgloss.NewCompositor(lipgloss.NewLayer(screen), lipgloss.NewLayer(box).X(x).Y(0).Z(1)).Render()
}
