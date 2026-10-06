package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestMarkdownSeparatesHeadingsAndCodeForReadability(t *testing.T) {
	m := testModel(t)
	m.setWidth(80)
	md := "Intro text.\n\n## 8. Switch\n\n```go\npackage main\n\nfunc main() {}\n```\n\nAfter the code.\n\nSecond paragraph."
	var got []string
	for _, l := range strings.Split(plain(m.renderMarkdown(md)), "\n") {
		got = append(got, strings.TrimSpace(l))
	}
	want := []string{
		"Intro text.",
		"", // separate prose from the section heading
		"## 8. Switch",
		"", // separate headings from code
		"package main",
		"", // blank lines inside code stay
		"func main() {}",
		"", // separate code from following prose
		"After the code.",
		"", // prose paragraph spacing is preserved
		"Second paragraph.",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestMarkdownTablesHaveACompleteFrame(t *testing.T) {
	m := testModel(t)
	const width = 64
	m.setWidth(width)
	md := "Before the table.\n\n" +
		"| Status | Description |\n" +
		"| --- | --- |\n" +
		"| Testing 🧪 | A deliberately long sentence that wraps inside its cell. |\n" +
		"| Unicode | 日本語 and unbreakableunbreakableunbreakableunbreakable |\n\n" +
		"After the table."
	got := plain(m.renderMarkdown(md))
	lines := strings.Split(got, "\n")

	start, end := -1, -1
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		if strings.HasPrefix(trimmed, "┌") {
			start = i
		}
		if start >= 0 && strings.HasPrefix(trimmed, "└") {
			end = i
			break
		}
	}
	if start < 0 || end <= start {
		t.Fatalf("table has no complete frame:\n%s", got)
	}
	tableWidth := lipgloss.Width(lines[start])
	for i, line := range lines {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("line %d is %d columns, want at most %d: %q", i, w, width, line)
		}
		if i >= start && i <= end && lipgloss.Width(line) != tableWidth {
			t.Errorf("table line %d is %d columns, want %d: %q", i, lipgloss.Width(line), tableWidth, line)
		}
	}
	joined := strings.Join(lines[start:end+1], "\n")
	for _, want := range []string{"┌", "┬", "┐", "├", "┼", "┤", "└", "┴", "┘"} {
		if !strings.Contains(joined, want) {
			t.Errorf("table is missing %q:\n%s", want, joined)
		}
	}
	if !strings.Contains(got, "Before the table.") || !strings.Contains(got, "After the table.") {
		t.Fatalf("surrounding prose was lost:\n%s", got)
	}
}

func TestSplitMarkdown(t *testing.T) {
	segs := splitMarkdown("# Title\ntext\n````md\n```go\nx\n```\n````\n- item\n   ```\n   nested\n   ```\n#hashtag\n```\nunclosed")
	var kinds []int
	for _, s := range segs {
		kinds = append(kinds, s.kind)
	}
	want := []int{mdHeading, mdProse, mdCode, mdProse, mdCode}
	if len(kinds) != len(want) {
		t.Fatalf("segments %+v", segs)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("segments %+v", segs)
		}
	}
	if !strings.Contains(segs[2].text, "```go") || !strings.HasSuffix(segs[2].text, "````") {
		t.Errorf("a longer fence should contain a shorter one: %q", segs[2].text)
	}
	if !strings.Contains(segs[3].text, "nested") || !strings.Contains(segs[3].text, "#hashtag") {
		t.Errorf("indented fences and #words stay prose: %q", segs[3].text)
	}
}
