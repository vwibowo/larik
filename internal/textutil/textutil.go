// Package textutil holds the small string helpers several packages use to
// summarize text for display.
package textutil

import (
	"fmt"
	"strings"
)

// FirstLine returns s up to its first newline.
func FirstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// FirstText returns the first non-empty line of s, trimmed and without
// Markdown heading marks.
func FirstText(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#")); l != "" {
			return l
		}
	}
	return ""
}

// OneLine collapses runs of whitespace (newlines included) to single spaces
// and clips the result to n runes, ending with "…" when it clips.
func OneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); n > 0 && len(r) > n {
		s = string(r[:n-1]) + "…"
	}
	return s
}

// Plural formats a count with its noun: "1 line", "3 lines".
func Plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
