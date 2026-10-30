package textutil

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestDiff(t *testing.T) {
	for _, c := range []struct{ name, old, new, want string }{
		{"one line", "BAD", "GOOD", "- BAD\n+ GOOD"},
		{"change inside", "a\nb\nc", "a\nx\nc", "  a\n- b\n+ x\n  c"},
		{"insert", "a\nc", "a\nb\nc", "  a\n+ b\n  c"},
		{"delete all", "a\nb", "", "- a\n- b"},
		{"add to empty", "", "a", "+ a"},
		{"shared final newline", "a\n", "b\n", "- a\n+ b"},
		{"new final newline", "a", "a\n", "  a\n+ "},
		{"same", "a\nb", "a\nb", ""},
		{"moved line", "a\nb\nc", "c\na\nb", "+ c\n  a\n  b\n- c"},
	} {
		if got := Diff(c.old, c.new); got != c.want {
			t.Errorf("%s: Diff(%q, %q) = %q, want %q", c.name, c.old, c.new, got, c.want)
		}
	}
}

func TestDiffTrimsUnchangedLines(t *testing.T) {
	var lines []string
	for i := range 20 {
		lines = append(lines, fmt.Sprint(i))
	}
	old := strings.Join(lines, "\n")
	lines[1], lines[17] = "first", "second"
	want := "  0\n- 1\n+ first\n  2\n  3\n  4\n  ⋯\n  14\n  15\n  16\n- 17\n+ second\n  18\n  19"
	if got := Diff(old, strings.Join(lines, "\n")); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDiffShowsALoneHiddenLine(t *testing.T) {
	old := "a\n1\n2\n3\nmid\n4\n5\n6\nb"
	new := "A\n1\n2\n3\nmid\n4\n5\n6\nB"
	want := "- a\n+ A\n  1\n  2\n  3\n  mid\n  4\n  5\n  6\n- b\n+ B"
	if got := Diff(old, new); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDiffPastTheCellCapStaysCorrect(t *testing.T) {
	var a, b []string
	for i := range 3000 {
		a = append(a, "old"+string(rune('a'+i%26)))
		b = append(b, "new"+string(rune('a'+i%26)))
	}
	got := strings.Split(Diff("top\n"+strings.Join(a, "\n"), "top\n"+strings.Join(b, "\n")), "\n")
	if len(got) != 6001 || got[0] != "  top" || got[1] != "- olda" || got[3001] != "+ newa" {
		t.Fatalf("got %d rows starting %q", len(got), got[:2])
	}
}

func TestWordDiff(t *testing.T) {
	a, b := WordDiff("var count = 1 + 2", "var total = 1 + 23")
	if want := []Span{{4, 9}, {16, 17}}; !reflect.DeepEqual(a, want) {
		t.Fatalf("old spans = %v, want %v", a, want)
	}
	if want := []Span{{4, 9}, {16, 18}}; !reflect.DeepEqual(b, want) {
		t.Fatalf("new spans = %v, want %v", b, want)
	}
}

func TestWordDiffJoinsAcrossSpaces(t *testing.T) {
	a, b := WordDiff("keep this a b", "keep this c d")
	if want := []Span{{10, 13}}; !reflect.DeepEqual(a, want) || !reflect.DeepEqual(b, want) {
		t.Fatalf("spans = %v, %v", a, b)
	}
}

func TestWordDiffSkipsUnrelatedLines(t *testing.T) {
	if a, b := WordDiff("return nil", "for i := range items {"); a != nil || b != nil {
		t.Fatalf("spans = %v, %v", a, b)
	}
}

func TestWordDiffSeesIndentation(t *testing.T) {
	a, b := WordDiff("x := 1", "\tx := 1")
	if a != nil || !reflect.DeepEqual(b, []Span{{0, 1}}) {
		t.Fatalf("spans = %v, %v", a, b)
	}
}

func TestWordDiffKeepsMultibyteRunesWhole(t *testing.T) {
	a, _ := WordDiff("naïve → café", "naïve ← café")
	if len(a) != 1 || "naïve → café"[a[0].Start:a[0].End] != "→" {
		t.Fatalf("spans = %v", a)
	}
}
