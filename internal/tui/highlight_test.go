package tui

import (
	"strings"
	"testing"

	"larik/internal/agent"
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
