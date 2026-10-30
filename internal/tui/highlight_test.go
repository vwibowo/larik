package tui

import (
	"strings"
	"testing"

	"larik/internal/agent"

	"charm.land/lipgloss/v2"
)

func TestCodeHighlightAndFallback(t *testing.T) {
	m := testModel(t)
	code := "func main() {}"
	dark, ok := m.colorCode("main.go", code)
	if !ok || !strings.Contains(dark, "\x1b[") || plain(dark) != code {
		t.Fatalf("Go highlight changed text or has no color: %q", dark)
	}
	m.applyTheme(false)
	light, ok := m.colorCode("main.go", code)
	if !ok || plain(light) != code || light == dark {
		t.Fatalf("light theme highlight: %q", light)
	}
	fallback, ok := m.colorCode("file.unknown-extension", code)
	if ok || fallback != code {
		t.Fatalf("unknown extension should stay plain: %q", fallback)
	}
}

func TestHighlightDiffMarksChangedWords(t *testing.T) {
	m := testModel(t)
	m.setWidth(80)
	diff := "  ctx\n- var count = 1\n+ var count = 2\n- gone\n+ a completely different line"
	for _, path := range []string{"main.go", "notes.unknown"} {
		out := m.highlightDiff(path, diff, 10)
		if got := plain(out); got != diff {
			t.Fatalf("%s: text changed: %q", path, got)
		}
		lines := strings.Split(out, "\n")
		for _, c := range []struct {
			line   int
			strong lipgloss.Style
			word   string
		}{{1, m.st.diffDelWord, "1"}, {2, m.st.diffAddWord, "2"}} {
			if !strings.Contains(lines[c.line], backgroundOf(c.strong)) {
				t.Errorf("%s: %q has no %q marked", path, lines[c.line], c.word)
			}
		}
		for _, i := range []int{3, 4} {
			for _, strong := range []lipgloss.Style{m.st.diffDelWord, m.st.diffAddWord} {
				if strings.Contains(lines[i], backgroundOf(strong)) {
					t.Errorf("%s: unrelated line %q is marked", path, lines[i])
				}
			}
		}
	}
}

// backgroundOf returns the SGR parameters that set st's background, which a
// styled run may share one escape sequence with its foreground.
func backgroundOf(st lipgloss.Style) string {
	styled := st.Render("x")
	return strings.TrimSuffix(strings.TrimPrefix(styled[:strings.Index(styled, "x")], "\x1b["), "m")
}

func TestEditApprovalShowsOnlyWhatChanged(t *testing.T) {
	m := testModel(t)
	m.setWidth(80)
	edit := agent.Event{ToolName: "edit", Input: []byte(`{"path":"main.go","old_string":"a\nb\nc","new_string":"a\nx\nc"}`)}
	if got := plain(m.permDetail(&edit)); !strings.Contains(got, "  a\n- b\n+ x\n  c") {
		t.Fatalf("approval preview: %q", got)
	}
}

func TestHighlightedEditAndWritePreviews(t *testing.T) {
	m := testModel(t)
	m.setWidth(80)
	diff := "- var count = 1\n+ var count = 2"
	if got := plain(m.highlightDiff("main.go", diff, 10)); got != diff {
		t.Fatalf("highlight changed diff text: %q", got)
	}
	edit := agent.Event{ToolName: "edit", Input: []byte(`{"path":"main.go","old_string":"var count = 1","new_string":"var count = 2"}`), Display: diff}
	for _, rendered := range []string{m.permDetail(&edit), m.renderToolCard(edit)} {
		if !strings.Contains(plain(rendered), "- var count = 1") || !strings.Contains(plain(rendered), "+ var count = 2") || !strings.Contains(rendered, "\x1b[") {
			t.Fatalf("edit preview lacks colored diff: %q", rendered)
		}
	}
	write := agent.Event{ToolName: "write", Input: []byte(`{"path":"main.go","content":"package main\nfunc main() {}\n"}`), Output: "Created main.go (2 lines)"}
	for _, rendered := range []string{m.permDetail(&write), m.renderToolCard(write)} {
		if !strings.Contains(plain(rendered), "+ package main") || !strings.Contains(plain(rendered), "+ func main() {}") || !strings.Contains(rendered, "\x1b[") {
			t.Fatalf("write preview lacks colored code: %q", rendered)
		}
	}
}
