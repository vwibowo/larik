package tui

import (
	"strings"
	"testing"
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
