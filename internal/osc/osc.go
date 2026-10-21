// Package osc writes the terminal escapes Larik uses to reach the user
// outside its own screen: desktop notifications.
package osc

import (
	"os"
	"strings"
)

// SupportsNotify reports whether the terminal shows OSC 9 as a desktop
// notification; others would ignore it.
func SupportsNotify() bool {
	switch os.Getenv("TERM_PROGRAM") {
	case "iTerm.app", "ghostty", "WezTerm":
		return true
	}
	return strings.Contains(os.Getenv("TERM"), "kitty") || os.Getenv("KITTY_WINDOW_ID") != "" ||
		os.Getenv("GHOSTTY_RESOURCES_DIR") != "" || os.Getenv("CMUX_SOCKET_PATH") != ""
}

// Notify is the OSC 9 escape that shows msg as a desktop notification.
func Notify(msg string) string { return "\x1b]9;" + Sanitize(msg) + "\x07" }

// Sanitize drops control characters that would end the sequence early.
func Sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}
