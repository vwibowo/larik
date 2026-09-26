package tools

import (
	"fmt"
	"unicode/utf8"
)

// MaxOutputBytes caps any tool output sent to the model.
const MaxOutputBytes = 30_000

// Truncate keeps the head and tail of s when it exceeds max bytes, since
// errors and summaries tend to live at the end of command output.
func Truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	head := safeCut(s, max*2/3)
	tail := s[len(s)-(max-len(head)):]
	for !utf8.ValidString(tail) && len(tail) > 0 {
		tail = tail[1:]
	}
	omitted := len(s) - len(head) - len(tail)
	return fmt.Sprintf("%s\n\n... [%d bytes truncated] ...\n\n%s", head, omitted, tail)
}

func safeCut(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
