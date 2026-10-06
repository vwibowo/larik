package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
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

// frameMarkdownTables completes the open table drawn by glamour. Glamour keeps
// only column separators and the header rule; adding the four outside edges
// makes those separators read as one table instead of dangling vertical rails.
func frameMarkdownTables(s string) string {
	lines := strings.Split(s, "\n")
	cursor := 0
	var out []string
	for i := 0; i < len(lines); i++ {
		indent, divider, ok := tableDivider(lines[i])
		if !ok {
			continue
		}
		width := lipgloss.Width(divider)
		start := i
		for start > cursor && tableContentLine(lines[start-1], indent) {
			start--
		}
		end := i
		for end+1 < len(lines) && tableContentLine(lines[end+1], indent) {
			end++
		}
		if start == i || end == i { // not a complete rendered table
			continue
		}

		out = append(out, lines[cursor:start]...)
		pad := strings.Repeat(" ", indent)
		out = append(out, pad+"┌"+strings.ReplaceAll(divider, "┼", "┬")+"┐")
		for row := start; row <= end; row++ {
			if row == i {
				out = append(out, pad+"├"+divider+"┤")
				continue
			}
			content := ansi.Cut(lines[row], indent, indent+width)
			if n := width - lipgloss.Width(content); n > 0 {
				content += strings.Repeat(" ", n)
			}
			out = append(out, pad+"│"+content+"│")
		}
		out = append(out, pad+"└"+strings.ReplaceAll(divider, "┼", "┴")+"┘")
		cursor = end + 1
		i = end
	}
	out = append(out, lines[cursor:]...)
	return strings.Join(out, "\n")
}

// tableDivider recognizes glamour's header rule and returns its indentation and
// visible rule. ANSI styling is ignored for detection.
func tableDivider(line string) (int, string, bool) {
	plain := strings.TrimRight(ansi.Strip(line), " ")
	trimmed := strings.TrimLeft(plain, " ")
	if !strings.Contains(trimmed, "┼") {
		return 0, "", false
	}
	for _, r := range trimmed {
		if r != '─' && r != '┼' {
			return 0, "", false
		}
	}
	return len(plain) - len(trimmed), trimmed, true
}

func tableContentLine(line string, indent int) bool {
	plain := ansi.Strip(line)
	return strings.Contains(plain, "│") && lipgloss.Width(plain) > indent
}
