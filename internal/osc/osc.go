// Package osc writes the terminal escapes Larik uses to reach the user
// outside its own screen: desktop notifications and program status.
package osc

import (
	"encoding/base64"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"larik/internal/status"
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

// statusID names Larik's OSC 7501 record, so clearing it leaves records
// other programs in the same terminal reported alone.
const statusID = "larik"

// OSC 7501 limits on the decoded title and message.
const (
	maxStatusTitle = 192
	maxStatusMsg   = 2048
)

// Status is the OSC 7501 escape that reports r as the terminal's program
// status for Larik. Terminals without OSC 7501 ignore it.
func Status(r status.Report) string {
	var b strings.Builder
	b.WriteString("\x1b]7501;id=" + statusID + ":app=larik:state=" + string(r.State))
	if r.State == status.Blocked && r.Kind != "" {
		b.WriteString(":kind=" + string(r.Kind))
	}
	if t := statusText(r.Title, maxStatusTitle); t != "" {
		b.WriteString(":title=" + t)
	}
	if m := statusText(r.Msg, maxStatusMsg); m != "" {
		b.WriteString(":msg=" + m)
	}
	b.WriteString("\x07")
	return b.String()
}

// ClearStatus is the OSC 7501 escape that removes Larik's record.
func ClearStatus() string { return "\x1b]7501;id=" + statusID + ":state=clear\x07" }

// statusText is s as OSC 7501 wants it: one line with no control
// characters, cut to max bytes on a rune boundary, base64-encoded.
func statusText(s string, max int) string {
	s = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)), " ")
	if len(s) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	if s == "" {
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte(s))
}
