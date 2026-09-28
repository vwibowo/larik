package tui

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"
)

// colorCode highlights a short snippet by filename. Unknown extensions and
// lexer errors leave the text untouched.
func (m *model) colorCode(path, code string) (string, bool) {
	lexer := lexers.Match(path)
	if lexer == nil {
		return code, false
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, code)
	if err != nil {
		return code, false
	}
	name := "github-dark"
	if !m.isDark {
		name = "github"
	}
	var out strings.Builder
	if err := formatters.TTY16m.Format(&out, chromastyles.Get(name), it); err != nil {
		return code, false
	}
	return out.String(), true
}

// highlightDiff colors code without losing the add/remove markers. The line
// and width bounds are applied before tokenizing tool-supplied content.
func (m *model) highlightDiff(path, diff string, maxLines int) string {
	lines := strings.Split(truncateLines(diff, maxLines), "\n")
	width := max(min(m.width-10, 160), 10)
	for i, line := range lines {
		line = ansi.Truncate(line, width, "…")
		if len(line) < 2 || (line[:2] != "+ " && line[:2] != "- ") {
			lines[i] = m.st.dim.Render(line)
			continue
		}
		marker, code := line[:2], line[2:]
		style := m.st.diffAdd
		if marker == "- " {
			style = m.st.diffDel
		}
		if colored, ok := m.colorCode(path, code); ok {
			lines[i] = style.Render(marker) + colored
		} else {
			lines[i] = style.Render(line)
		}
	}
	return strings.Join(lines, "\n")
}
