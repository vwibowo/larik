package tui

import (
	"time"

	"larik/internal/osc"

	tea "charm.land/bubbletea/v2"
)

// longTurn is how long a turn must run before its end is worth a
// notification.
const longTurn = 10 * time.Second

// alert tells the user larik needs them, per the notifications setting.
// It stays quiet while the terminal has focus: they are already looking.
// A terminal that never reports focus gets every alert.
func (m *model) alert(msg string) tea.Cmd {
	if (m.focusKnown && m.focused) || m.notify == "off" || m.notify == "" {
		return nil
	}
	if m.notify == "desktop" && osc.SupportsNotify() {
		return tea.Raw(osc.Notify("larik " + msg))
	}
	return tea.Raw("\a")
}
