package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
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
		got, err := applyHunk(old, new, hs[c.h], c.pick, c.forward)
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
		if got, _ := applyHunk(c.old, c.new, hs[0], nil, true); got != c.new {
			t.Errorf("%q → %q staged: %q", c.old, c.new, got)
		}
		if got, _ := applyHunk(c.old, c.new, hs[0], nil, false); got != c.old {
			t.Errorf("%q → %q discarded: %q", c.old, c.new, got)
		}
	}

	// CRLF stays CRLF.
	crOld, crNew := "a\r\nb\r\nc\r\n", "a\r\nB\r\nc\r\n"
	hs = hunksOf(crOld, crNew)
	if got, _ := applyHunk(crOld, crNew, hs[0], nil, true); got != crNew {
		t.Errorf("crlf: %q", got)
	}
	// A change that isn't in the texts.
	if _, err := applyHunk("a\n", "b\n", editor.DiffHunk{OldStart: 0, OldEnd: 9, NewStart: 0, NewEnd: 1}, nil, true); err == nil {
		t.Error("a change past the end applied")
	}
}

func TestHunkStaging(t *testing.T) {
	repo := gitRepo(t)
	file := filepath.Join(repo, "a.txt")
	lines := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"
	os.WriteFile(file, []byte(lines), 0o644)
	os.WriteFile(filepath.Join(repo, "run.sh"), []byte("echo one\n"), 0o755)
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-qm", "a")
	// Two changes, far apart, and one to a script.
	os.WriteFile(file, []byte("1\nTWO\n3\n4\n5\n6\n7\n8\n9\nTEN\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "run.sh"), []byte("echo one\necho two\n"), 0o755)

	a, mu := serialApp(Options{})
	ch := &changes{dir: repo, stop: make(chan struct{}), kick: make(chan struct{}, 1)}
	read := func() {
		t.Helper()
		r := readChanges(repo, headCache{}, ch.against)
		mu.Lock()
		ch.apply(r)
		mu.Unlock()
	}
	fileOf := func(path string) *fileChange {
		for _, f := range ch.files {
			if f.path == path {
				return f
			}
		}
		t.Fatalf("no %s in %d files", path, len(ch.files))
		return nil
	}
	done := func(what string) {
		t.Helper()
		waitFor(t, mu, what, func() bool { return ch.busy == "" })
		mu.Lock()
		notice := a.notice
		mu.Unlock()
		if notice != "" {
			t.Fatalf("%s: %s", what, notice)
		}
		read()
	}
	read()
	f := fileOf("a.txt")
	if len(f.parts) != 1 || f.parts[0].staged || !f.parts[0].diff.HunkActions || len(f.parts[0].diff.Hunks()) != 2 {
		t.Fatalf("unstaged a.txt: %d parts", len(f.parts))
	}

	// Stage the first change: two diffs now, as git has them.
	mu.Lock()
	p := f.parts[0]
	a.hunkDo(ch, f, p, p.diff.Hunks()[0], stageHunk)
	mu.Unlock()
	done("staging the first change")
	if got := gitIn(t, repo, "diff", "--cached", "--", "a.txt"); !strings.Contains(got, "+TWO") || strings.Contains(got, "+TEN") {
		t.Fatalf("staged: %s", got)
	}
	if got := gitIn(t, repo, "diff", "--", "a.txt"); !strings.Contains(got, "+TEN") || strings.Contains(got, "+TWO") {
		t.Fatalf("not staged: %s", got)
	}
	f = fileOf("a.txt")
	if f.code != "MM" || len(f.parts) != 2 || f.parts[0].staged || !f.parts[1].staged {
		t.Fatalf("partly staged: %q, %d parts", f.code, len(f.parts))
	}
	pane := ui.NewTester(func(c *ui.Context) { a.changesPane(c, a.palette(ui.DarkTheme()), ch) }, 640, 900)
	pane.SetDark(true)
	mu.Lock()
	pane.Frame()
	if !pane.HasText("Unstaged") || !pane.HasText("Staged") {
		mu.Unlock()
		t.Fatalf("two diffs: %q", pane.Texts())
	}
	snapshot(t, pane, "hunks-dark.png")
	// The change under the pointer has its buttons: Stage and Discard on
	// what isn't staged, Unstage on what is.
	pane.Move(300, 324) // TEN
	pane.Frame()
	_, stage := pane.Find("Stage a.txt")
	_, discard := pane.Find("Discard a.txt")
	pane.Move(300, 418) // TWO
	pane.Frame()
	_, unstage := pane.Find("Unstage a.txt")
	if !stage || !discard || !unstage {
		mu.Unlock()
		t.Fatalf("buttons: stage %v, discard %v, unstage %v: %q", stage, discard, unstage, pane.Texts())
	}
	snapshot(t, pane, "hunks-hover-dark.png")

	// Unstage it again, with the staged diff's button.
	if err := pane.Click("Unstage a.txt"); err != nil {
		mu.Unlock()
		t.Fatal(err)
	}
	mu.Unlock()
	done("unstaging it")
	if got := gitIn(t, repo, "diff", "--cached"); got != "" {
		t.Fatalf("still staged: %s", got)
	}

	// Discard the second, which asks first; the first stays.
	f = fileOf("a.txt")
	mu.Lock()
	p = f.parts[0]
	a.hunkDo(ch, f, p, p.diff.Hunks()[1], discardHunk)
	if a.asked == nil || !strings.Contains(a.asked.title, "Discard this change") {
		mu.Unlock()
		t.Fatalf("discarding asks: %+v", a.asked)
	}
	a.asked.then()
	a.asked = nil
	mu.Unlock()
	done("discarding the second")
	if got, _ := os.ReadFile(file); string(got) != "1\nTWO\n3\n4\n5\n6\n7\n8\n9\n10\n" {
		t.Fatalf("discarded: %q", got)
	}

	// Lines: of TWO for 2, stage the addition alone.
	f = fileOf("a.txt")
	mu.Lock()
	p = f.parts[0]
	h := p.diff.Hunks()[0]
	p.diff.SetSelectedLines(editor.DiffLineRange{Side: editor.DiffNew, Start: h.NewStart, End: h.NewStart}, true)
	if hunkPick(p.diff, h) == nil || stageHunk.label(true) != "Stage Lines" {
		mu.Unlock()
		t.Fatal("lines chosen in the change pick them")
	}
	a.hunkDo(ch, f, p, h, stageHunk)
	mu.Unlock()
	done("staging a line")
	if got := gitIn(t, repo, "show", ":a.txt"); got != "1\n2\nTWO\n3\n4\n5\n6\n7\n8\n9\n10" {
		t.Fatalf("a line staged: %q", got)
	}

	// The script keeps its mode in the index.
	s := fileOf("run.sh")
	mu.Lock()
	p = s.parts[0]
	a.hunkDo(ch, s, p, p.diff.Hunks()[0], stageHunk)
	mu.Unlock()
	done("staging the script")
	if got := gitIn(t, repo, "ls-files", "-s", "--", "run.sh"); !strings.HasPrefix(got, "100755 ") {
		t.Fatalf("mode: %s", got)
	}

	// A new file, staged, then changed: unstaging all that's staged of it
	// leaves it untracked.
	nf := filepath.Join(repo, "n.txt")
	os.WriteFile(nf, []byte("one\n"), 0o644)
	gitIn(t, repo, "add", "n.txt")
	os.WriteFile(nf, []byte("one\ntwo\n"), 0o644)
	read()
	n := fileOf("n.txt")
	if n.code != "AM" || len(n.parts) != 2 || !n.parts[1].staged || n.parts[1].old != "" {
		t.Fatalf("new file: %q, %d parts", n.code, len(n.parts))
	}
	mu.Lock()
	p = n.parts[1]
	a.hunkDo(ch, n, p, p.diff.Hunks()[0], unstageHunk)
	mu.Unlock()
	done("unstaging the new file")
	if n := fileOf("n.txt"); n.code != "??" {
		t.Fatalf("unstaged new file: %q", n.code)
	}

	// What changed since it was read isn't touched.
	f = fileOf("a.txt")
	unstagedPart := f.parts[0]
	os.WriteFile(file, []byte("moved on\n"), 0o644)
	mu.Lock()
	a.hunkDo(ch, f, unstagedPart, unstagedPart.diff.Hunks()[0], discardHunk)
	a.asked.then()
	a.asked = nil
	mu.Unlock()
	waitFor(t, mu, "the refusal", func() bool { return ch.busy == "" })
	if !strings.Contains(a.notice, "changed since it was read") {
		t.Fatalf("a file that moved on: %q", a.notice)
	}
	if got, _ := os.ReadFile(file); string(got) != "moved on\n" {
		t.Fatalf("overwritten: %q", got)
	}
}
