package tui

import (
	"os"
	"strings"
	"time"

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
	if m.notify == "desktop" && supportsOSC9() {
		return tea.Raw("\x1b]9;larik " + sanitizeOSC(msg) + "\x07")
	}
	return tea.Raw("\a")
}

// supportsOSC9 reports whether the terminal shows OSC 9 as a desktop
// notification; others would ignore it, so they get the bell instead.
func supportsOSC9() bool {
	switch os.Getenv("TERM_PROGRAM") {
	case "iTerm.app", "ghostty", "WezTerm":
		return true
	}
	return strings.Contains(os.Getenv("TERM"), "kitty") || os.Getenv("KITTY_WINDOW_ID") != ""
}

// sanitizeOSC drops control characters that would end the sequence early.
func sanitizeOSC(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}
