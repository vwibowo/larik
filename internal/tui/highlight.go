package tui

import (
	"strings"

	"larik/internal/textutil"

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
	return m.colorMarked(path, code, base, lipgloss.NewStyle(), nil)
}

// colorMarked is colorLines with marks[i], byte ranges of line i, painted on
// strong's background.
func (m *model) colorMarked(path, code string, base, strong lipgloss.Style, marks [][]textutil.Span) ([]string, bool) {
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
	col := 0
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
				col = 0
			}
			if seg == "" {
				continue
			}
			var spans []textutil.Span
			if line := len(out) - 1; line < len(marks) {
				spans = marks[line]
			}
			out[len(out)-1] += renderMarked(seg, col, spans, st, st.Background(strong.GetBackground()))
			col += len(seg)
		}
	}
	if len(out) < want {
		return nil, false
	}
	return out[:want], true // lexers may add a final newline
}

// renderMarked renders seg, which starts at byte col of its line, in st,
// with the parts inside spans in strong.
func renderMarked(seg string, col int, spans []textutil.Span, st, strong lipgloss.Style) string {
	var b strings.Builder
	at := 0
	for _, s := range spans {
		start, end := min(max(s.Start-col, at), len(seg)), min(max(s.End-col, at), len(seg))
		if start >= end {
			continue
		}
		if start > at {
			b.WriteString(st.Render(seg[at:start]))
		}
		b.WriteString(strong.Render(seg[start:end]))
		at = end
	}
	if at < len(seg) {
		b.WriteString(st.Render(seg[at:]))
	}
	return b.String()
}

// highlightDiff colors "+ " and "- " lines as code, tinted by side, marks the
// words that changed, and dims the rest. Consecutive lines of one side are
// highlighted together.
func (m *model) highlightDiff(path, diff string, maxLines int) string {
	lines := strings.Split(truncateLines(diff, maxLines), "\n")
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], m.diffWidth(), "…")
	}
	return m.highlightLines(path, lines)
}

// diffWidth is how much of each line a diff preview shows.
func (m *model) diffWidth() int {
	return max(min(m.width-10, 160), 10)
}

// highlightLines is highlightDiff without any clipping.
func (m *model) highlightLines(path string, lines []string) string {
	marks := wordMarks(lines)
	out := make([]string, len(lines))
	for i := 0; i < len(lines); {
		marker := diffMarker(lines[i])
		if marker == "" {
			out[i] = m.st.dim.Render(lines[i])
			i++
			continue
		}
		j := i
		var code []string
		for j < len(lines) && diffMarker(lines[j]) == marker {
			code = append(code, lines[j][len(marker):])
			j++
		}
		side, bg, strong := m.st.diffAdd, m.st.diffAddBg, m.st.diffAddWord
		if marker == "- " {
			side, bg, strong = m.st.diffDel, m.st.diffDelBg, m.st.diffDelWord
		}
		colored, ok := m.colorMarked(path, strings.Join(code, "\n"), bg, strong, marks[i:j])
		for k, c := range code {
			if ok {
				out[i+k] = side.Render(marker) + colored[k]
			} else {
				out[i+k] = side.Render(marker) + renderMarked(c, 0, marks[i+k], bg.Inherit(side), strong.Inherit(side))
			}
		}
		i = j
	}
	return strings.Join(out, "\n")
}

// wordMarks pairs each block of removed lines with the added lines right
// after it, first with first, and returns the changed spans by line index.
func wordMarks(lines []string) [][]textutil.Span {
	marks := make([][]textutil.Span, len(lines))
	for i := 0; i < len(lines); {
		if diffMarker(lines[i]) != "- " {
			i++
			continue
		}
		added := i
		for added < len(lines) && diffMarker(lines[added]) == "- " {
			added++
		}
		end := added
		for end < len(lines) && diffMarker(lines[end]) == "+ " {
			end++
		}
		for k := 0; k < min(added-i, end-added); k++ {
			marks[i+k], marks[added+k] = textutil.WordDiff(lines[i+k][2:], lines[added+k][2:])
		}
		i = end
	}
	return marks
}

func diffMarker(line string) string {
	if strings.HasPrefix(line, "+ ") || strings.HasPrefix(line, "- ") {
		return line[:2]
	}
	return ""
}
