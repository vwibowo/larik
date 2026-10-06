package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"
)

// colorCode highlights a short snippet by filename. Unknown extensions and
// lexer errors leave the text untouched.
func (m *model) colorCode(path, code string) (string, bool) {
	lines, ok := m.colorLines(path, code, lipgloss.NewStyle())
	if !ok {
		return code, false
	}
	return strings.Join(lines, "\n"), true
}

// colorLines highlights code as one block, so strings and comments that
// span lines color correctly, and returns it split into lines, each styled
// on its own over base (e.g. a diff background).
func (m *model) colorLines(path, code string, base lipgloss.Style) ([]string, bool) {
	lexer := lexers.Match(path)
	if lexer == nil {
		return nil, false
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, code)
	if err != nil {
		return nil, false
	}
	name := "github-dark"
	if !m.isDark {
		name = "github"
	}
	style := chromastyles.Get(name)
	want := strings.Count(code, "\n") + 1
	out := []string{""}
	for tok := it(); tok != chroma.EOF; tok = it() {
		st := base
		entry := style.Get(tok.Type)
		colour := ""
		if m.themePalette != nil {
			colour = m.themePalette.ANSI[7]
			kind := strings.ToLower(tok.Type.String())
			switch {
			case strings.Contains(kind, "comment"):
				colour = m.themePalette.ANSI[8]
			case strings.Contains(kind, "string"):
				colour = m.themePalette.ANSI[2]
			case strings.Contains(kind, "keyword") || strings.Contains(kind, "operator"):
				colour = m.themePalette.ANSI[5]
			case strings.Contains(kind, "number") || strings.Contains(kind, "literal"):
				colour = m.themePalette.ANSI[3]
			}
		} else if entry.Colour.IsSet() {
			colour = entry.Colour.String()
		}
		if colour != "" {
			st = st.Foreground(lipgloss.Color(colour))
		}
		st = st.Bold(entry.Bold == chroma.Yes).Italic(entry.Italic == chroma.Yes)
		for i, seg := range strings.Split(tok.Value, "\n") {
			if i > 0 {
				out = append(out, "")
			}
			if seg != "" {
				out[len(out)-1] += st.Render(seg)
			}
		}
	}
	if len(out) < want {
		return nil, false
	}
	return out[:want], true // lexers may add a final newline
}

// highlightDiff colors "+ " and "- " lines as code, tinted by side, and
// dims the rest. Consecutive lines of one side are highlighted together.
func (m *model) highlightDiff(path, diff string, maxLines int) string {
	lines := strings.Split(truncateLines(diff, maxLines), "\n")
	width := max(min(m.width-10, 160), 10)
	out := make([]string, len(lines))
	for i := 0; i < len(lines); {
		marker := diffMarker(lines[i])
		if marker == "" {
			out[i] = m.st.dim.Render(ansi.Truncate(lines[i], width, "…"))
			i++
			continue
		}
		j := i
		var code []string
		for j < len(lines) && diffMarker(lines[j]) == marker {
			code = append(code, ansi.Truncate(lines[j], width, "…")[len(marker):])
			j++
		}
		side, bg := m.st.diffAdd, m.st.diffAddBg
		if marker == "- " {
			side, bg = m.st.diffDel, m.st.diffDelBg
		}
		colored, ok := m.colorLines(path, strings.Join(code, "\n"), bg)
		for k, c := range code {
			if ok {
				out[i+k] = side.Render(marker) + colored[k]
			} else {
				out[i+k] = side.Render(marker) + bg.Inherit(side).Render(c)
			}
		}
		i = j
	}
	return strings.Join(out, "\n")
}

func diffMarker(line string) string {
	if strings.HasPrefix(line, "+ ") || strings.HasPrefix(line, "- ") {
		return line[:2]
	}
	return ""
}
