package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// mdSegment is a run of markdown rendered on its own: a heading, a fenced
// code block, or the prose between them.
type mdSegment struct {
	kind int
	text string
}

const (
	mdProse = iota
	mdHeading
	mdCode
)

// splitMarkdown cuts top-level ATX headings and fenced code blocks out of
// s. Glamour puts a blank line after every heading and around every code
// block; rendering the pieces apart lets us join them tighter. Indented
// fences (e.g. inside list items) stay part of the prose.
func splitMarkdown(s string) []mdSegment {
	var segs []mdSegment
	var prose []string
	flush := func() {
		if t := strings.TrimSpace(strings.Join(prose, "\n")); t != "" {
			segs = append(segs, mdSegment{mdProse, t})
		}
		prose = nil
	}
	lines := strings.Split(s, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if fence := fenceOf(line); fence != "" {
			flush()
			j := i + 1
			for j < len(lines) && !strings.HasPrefix(strings.TrimRight(lines[j], " \t"), fence) {
				j++
			}
			end := min(j, len(lines)-1) // an unclosed fence runs to the end
			segs = append(segs, mdSegment{mdCode, strings.Join(lines[i:end+1], "\n")})
			i = end
			continue
		}
		if isATXHeading(line) {
			flush()
			segs = append(segs, mdSegment{mdHeading, line})
			continue
		}
		prose = append(prose, line)
	}
	flush()
	return segs
}

// fenceOf returns the fence (``` or ~~~, as long as written) opening a
// code block at the start of line, or "".
func fenceOf(line string) string {
	for _, c := range []string{"`", "~"} {
		n := len(line) - len(strings.TrimLeft(line, c))
		if n >= 3 && !(c == "`" && strings.Contains(line[n:], "`")) {
			return line[:n]
		}
	}
	return ""
}

func isATXHeading(line string) bool {
	n := len(line) - len(strings.TrimLeft(line, "#"))
	return n >= 1 && n <= 6 && (len(line) == n || line[n] == ' ' || line[n] == '\t')
}

// trimBlankLines drops leading and trailing lines that render empty;
// glamour pads them with spaces, so trimming newlines isn't enough.
func trimBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	blank := func(l string) bool { return strings.TrimSpace(ansi.Strip(l)) == "" }
	for len(lines) > 0 && blank(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && blank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}
