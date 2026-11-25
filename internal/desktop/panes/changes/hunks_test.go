package changespane

import (
	"testing"

	"github.com/egoist/mygo/ui/editor"
)

// hunksOf are the changes the diff viewer finds from old to new.
func hunksOf(old, new string) []editor.DiffHunk {
	d := &editor.DiffState{Old: editor.NewBuffer(old), New: editor.NewBuffer(new)}
	return d.Hunks()
}

func TestApplyHunk(t *testing.T) {
	old := "a\nb\nc\nd\ne\nf\ng\nh\n"
	new := "a\nB\nc\nd\ne\nf\ng\nh\ni\n"
	hs := hunksOf(old, new)
	if len(hs) != 2 {
		t.Fatalf("hunks: %+v", hs)
	}
	for _, c := range []struct {
		name    string
		h       int
		pick    func(editor.DiffSide, int) bool
		forward bool
		on      string // what it's applied to: old forward, new backward
		want    string
	}{
		{"stage the first", 0, nil, true, old, "a\nB\nc\nd\ne\nf\ng\nh\n"},
		{"stage the second", 1, nil, true, old, "a\nb\nc\nd\ne\nf\ng\nh\ni\n"},
		{"discard the first", 0, nil, false, new, "a\nb\nc\nd\ne\nf\ng\nh\ni\n"},
		{"discard the second", 1, nil, false, new, "a\nB\nc\nd\ne\nf\ng\nh\n"},
		// The addition alone of b → B: b stays, and B comes after it.
		{"stage the added line", 0, func(s editor.DiffSide, _ int) bool { return s == editor.DiffNew }, true, old, "a\nb\nB\nc\nd\ne\nf\ng\nh\n"},
		// The deletion alone: b goes, B isn't added.
		{"stage the deleted line", 0, func(s editor.DiffSide, _ int) bool { return s == editor.DiffOld }, true, old, "a\nc\nd\ne\nf\ng\nh\n"},
		// Taking back the deletion alone: b comes back, B stays.
		{"discard the deleted line", 0, func(s editor.DiffSide, _ int) bool { return s == editor.DiffOld }, false, new, "a\nb\nB\nc\nd\ne\nf\ng\nh\ni\n"},
	} {
		got, err := ApplyHunk(old, new, hs[c.h], c.pick, c.forward)
		if err != nil || got != c.want {
			t.Errorf("%s: %q, %v; want %q", c.name, got, err, c.want)
		}
	}

	// Ends without a newline: added, taken away, and lines after the last.
	for _, c := range []struct{ old, new string }{
		{"x", "x\ny\n"},
		{"x\n", "x\ny"},
		{"x\ny", "x\nz"},
		{"", "first\n"},
		{"gone\n", ""},
	} {
		hs := hunksOf(c.old, c.new)
		if len(hs) != 1 {
			t.Fatalf("%q → %q: %+v", c.old, c.new, hs)
		}
		if got, _ := ApplyHunk(c.old, c.new, hs[0], nil, true); got != c.new {
			t.Errorf("%q → %q staged: %q", c.old, c.new, got)
		}
		if got, _ := ApplyHunk(c.old, c.new, hs[0], nil, false); got != c.old {
			t.Errorf("%q → %q discarded: %q", c.old, c.new, got)
		}
	}

	// CRLF stays CRLF.
	crOld, crNew := "a\r\nb\r\nc\r\n", "a\r\nB\r\nc\r\n"
	hs = hunksOf(crOld, crNew)
	if got, _ := ApplyHunk(crOld, crNew, hs[0], nil, true); got != crNew {
		t.Errorf("crlf: %q", got)
	}
	// A change that isn't in the texts.
	if _, err := ApplyHunk("a\n", "b\n", editor.DiffHunk{OldStart: 0, OldEnd: 9, NewStart: 0, NewEnd: 1}, nil, true); err == nil {
		t.Error("a change past the end applied")
	}
}
