package checkpoint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// change captures path with capture, then writes content, the way a file
// tool does.
func change(t *testing.T, capture func(string) error, path, content string) {
	t.Helper()
	if err := capture(path); err != nil {
		t.Fatal(err)
	}
	write(t, path, content)
}

func wantErr(t *testing.T, err error, part string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), part) {
		t.Fatalf("error = %v, want one mentioning %q", err, part)
	}
}

func files(t *testing.T, work string, want map[string]string) {
	t.Helper()
	for name, content := range want {
		got := "<missing>"
		if data, err := os.ReadFile(filepath.Join(work, name)); err == nil {
			got = string(data)
		}
		if got != content {
			t.Errorf("%s = %q, want %q", name, got, content)
		}
	}
}

func TestTaskChangesStayOutOfLaterTurns(t *testing.T) {
	work, dir := t.TempDir(), filepath.Join(t.TempDir(), "sess")
	a, b, c := filepath.Join(work, "a"), filepath.Join(work, "b"), filepath.Join(work, "c")
	write(t, a, "a0")
	write(t, b, "b0")
	write(t, c, "c0")
	s := New(dir, work)

	s.BeginTurn() // turn 1 starts a background task
	bg := s.Begin("bg")
	change(t, s.Capture, a, "a1")
	s.BeginTurn() // turn 2 while the task still runs
	change(t, bg.Capture, b, "b1")
	change(t, s.Capture, c, "c1")

	_, err := s.Undo()
	wantErr(t, err, `task "bg" is still running`)
	if err := bg.End(); err != nil {
		t.Fatal(err)
	}
	if paths, err := s.Undo(); err != nil || len(paths) != 1 || paths[0] != c {
		t.Fatalf("undo turn 2: %v %v", paths, err)
	}
	files(t, work, map[string]string{"a": "a1", "b": "b1", "c": "c0"})
	if paths, err := s.Undo(); err != nil || len(paths) != 1 || paths[0] != b {
		t.Fatalf("undo the task: %v %v", paths, err)
	}
	files(t, work, map[string]string{"b": "b0"})
	if paths, err := s.Undo(); err != nil || len(paths) != 1 || paths[0] != a {
		t.Fatalf("undo turn 1: %v %v", paths, err)
	}
	files(t, work, map[string]string{"a": "a0"})
	wantErr(t, func() error { _, err := s.Undo(); return err }(), "nothing to undo")
}

// A task and a turn changing the same file can't be told apart afterwards:
// undoing either alone would discard the other's change, whichever went
// first, so it is refused; rewinding past both restores them together.
func TestUndoRefusesToDiscardAnotherUnitsChange(t *testing.T) {
	for _, taskFirst := range []bool{true, false} {
		work, dir := t.TempDir(), filepath.Join(t.TempDir(), "sess")
		p := filepath.Join(work, "p")
		write(t, p, "p0")
		s := New(dir, work)
		s.BeginTurn()
		start := time.Now()
		bg := s.Begin("bg")
		s.BeginTurn()
		if taskFirst {
			change(t, bg.Capture, p, "task")
			change(t, s.Capture, p, "turn")
		} else {
			change(t, s.Capture, p, "turn")
			change(t, bg.Capture, p, "task")
		}
		before := read(t, p)

		_, err := s.Undo()
		wantErr(t, err, `task "bg" is still running and has changed p`)
		_, err = s.UndoSince(start)
		wantErr(t, err, "still running")
		if read(t, p) != before {
			t.Fatal("a refused undo changed the file")
		}

		if err := bg.End(); err != nil {
			t.Fatal(err)
		}
		_, err = s.Undo()
		wantErr(t, err, `task "bg" also changed p`)
		// The refusal holds in a resumed session too.
		s = New(dir, work)
		_, err = s.Undo()
		wantErr(t, err, "also changed p")
		if read(t, p) != before {
			t.Fatal("a refused undo changed the file")
		}

		if got := s.Since(start); len(got) != 1 || got[0] != p {
			t.Fatalf("Since = %v", got)
		}
		if paths, err := s.UndoSince(start); err != nil || len(paths) != 1 {
			t.Fatalf("UndoSince: %v %v", paths, err)
		}
		if got := read(t, p); got != "p0" {
			t.Fatalf("taskFirst=%v: p = %q, want the state before both", taskFirst, got)
		}
	}
}

func TestFinishedSubagentsJoinTheirTurn(t *testing.T) {
	work, dir := t.TempDir(), filepath.Join(t.TempDir(), "sess")
	a, c, g := filepath.Join(work, "a"), filepath.Join(work, "c"), filepath.Join(work, "g")
	write(t, a, "a0")
	s := New(dir, work)
	s.BeginTurn()
	change(t, s.Capture, a, "a1")
	s.BeginTurn()
	child := s.Begin("child")
	grandchild := child.Begin("grandchild")
	change(t, grandchild.Capture, g, "g")
	if err := grandchild.End(); err != nil { // joins the child, which is still open
		t.Fatal(err)
	}
	change(t, child.Capture, c, "c")
	if err := child.End(); err != nil { // joins the turn, with the grandchild
		t.Fatal(err)
	}
	change(t, s.Capture, a, "a2")
	if err := child.End(); err != nil {
		t.Fatal("ending twice should do nothing:", err)
	}

	s = New(dir, work) // the grouping is saved
	paths, err := s.Undo()
	if err != nil || len(paths) != 3 {
		t.Fatalf("undo the turn and what it delegated: %v %v", paths, err)
	}
	files(t, work, map[string]string{"a": "a1", "c": "<missing>", "g": "<missing>"})
	if paths, err := s.Undo(); err != nil || len(paths) != 1 || read(t, a) != "a0" {
		t.Fatalf("undo turn 1: %v %v", paths, err)
	}
}

func TestTaskOutlivingItsTurnIsUndoneOnItsOwn(t *testing.T) {
	work, dir := t.TempDir(), filepath.Join(t.TempDir(), "sess")
	a, b, c := filepath.Join(work, "a"), filepath.Join(work, "b"), filepath.Join(work, "c")
	s := New(dir, work)
	s.BeginTurn()
	bg := s.Begin("bg")
	change(t, s.Capture, a, "a")
	s.BeginTurn()
	change(t, bg.Capture, b, "b")
	if err := bg.End(); err != nil { // turn 1 is over: stays its own unit
		t.Fatal(err)
	}
	change(t, s.Capture, c, "c")

	s = New(dir, work)
	for _, want := range []string{c, b, a} {
		paths, err := s.Undo()
		if err != nil || len(paths) != 1 || paths[0] != want {
			t.Fatalf("undo: %v %v, want %s", paths, err, want)
		}
		if _, err := os.Stat(want); !os.IsNotExist(err) {
			t.Fatalf("%s should be gone", want)
		}
	}
}

// Equal preimages do not prove that a shell was read-only: it may write
// after another unit captured the file. Preserve both owners conservatively.
func TestShellRecordOfAnotherUnitsChange(t *testing.T) {
	work, dir := t.TempDir(), filepath.Join(t.TempDir(), "sess")
	q := filepath.Join(work, "q")
	write(t, q, "q0")
	s := New(dir, work)
	s.BeginTurn()
	bg := s.Begin("bg")
	change(t, s.Capture, q, "q1") // the turn edits q while the task's command runs
	if err := bg.Record(q, true, []byte("q0"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.BeginTurn()
	if err := bg.End(); err != nil {
		t.Fatal(err)
	}
	if paths, err := s.Undo(); err == nil || len(paths) != 0 || read(t, q) != "q1" {
		t.Fatalf("ambiguous ownership must refuse: %v %v, q=%q", paths, err, read(t, q))
	}
	if paths, err := s.UndoSince(time.Time{}); err != nil || len(paths) != 1 || read(t, q) != "q0" {
		t.Fatalf("rewind both: %v %v, q=%q", paths, err, read(t, q))
	}
}

// A command that found a file in another state than the unit that captured
// it changed it too, perhaps before the capture: the state recorded may be
// from any time since its unit began.
func TestShellRecordOfAnOverlappingChange(t *testing.T) {
	work, dir := t.TempDir(), filepath.Join(t.TempDir(), "sess")
	r := filepath.Join(work, "r")
	write(t, r, "r0")
	s := New(dir, work)
	s.BeginTurn()
	bg := s.Begin("bg")
	write(t, r, "r-task")         // the task's command changes r...
	change(t, s.Capture, r, "r1") // ...then the turn edits it...
	// ...and the command, finishing, records the state it started from.
	if err := bg.Record(r, true, []byte("r0"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.BeginTurn()
	if err := bg.End(); err != nil {
		t.Fatal(err)
	}
	_, err := s.Undo() // the task, newest
	wantErr(t, err, "another turn also changed r")
	files(t, work, map[string]string{"r": "r1"})
	if paths, err := s.UndoSince(time.Time{}); err != nil || len(paths) != 1 {
		t.Fatalf("UndoSince: %v %v", paths, err)
	}
	files(t, work, map[string]string{"r": "r0"})
}

func TestChangeAfterAUnitEndsStaysUndoable(t *testing.T) {
	work, dir := t.TempDir(), filepath.Join(t.TempDir(), "sess")
	x, y := filepath.Join(work, "x"), filepath.Join(work, "y")
	s := New(dir, work)
	s.BeginTurn()
	u := s.Begin("bg")
	if err := u.End(); err != nil { // nothing changed: dropped
		t.Fatal(err)
	}
	s.BeginTurn()
	// An interrupted command finishes after the task ended.
	write(t, x, "late")
	if err := u.Record(x, false, nil, 0); err != nil {
		t.Fatal(err)
	}
	// It is still writing as far as undo can tell: the turn's change to x
	// is not separable from it.
	change(t, s.Capture, y, "y")
	if paths, err := s.Undo(); err != nil || len(paths) != 1 || paths[0] != y {
		t.Fatalf("undo the turn: %v %v", paths, err)
	}
	if paths, err := s.Undo(); err != nil || len(paths) != 1 || paths[0] != x {
		t.Fatalf("undo the late change: %v %v", paths, err)
	}
	files(t, work, map[string]string{"x": "<missing>", "y": "<missing>"})
}

func TestUndoRefusesTaskBeforeFirstSnapshot(t *testing.T) {
	work := t.TempDir()
	s := New(filepath.Join(t.TempDir(), "checkpoints"), work)
	s.BeginTurn()
	p := filepath.Join(work, "p")
	change(t, s.Capture, p, "foreground")
	bg := s.Begin("shell not recorded yet")
	if paths, err := s.Undo(); err == nil || len(paths) != 0 {
		t.Fatalf("undo while writer untracked: %v %v", paths, err)
	}
	if paths, err := s.UndoSince(time.Time{}); err == nil || len(paths) != 0 {
		t.Fatalf("rewind while writer untracked: %v %v", paths, err)
	}
	files(t, work, map[string]string{"p": "foreground"})
	if err := bg.End(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Undo(); err != nil {
		t.Fatal(err)
	}
}
