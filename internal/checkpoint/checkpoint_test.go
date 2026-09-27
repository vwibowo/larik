package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestUndoAcrossRuns(t *testing.T) {
	work, dir := t.TempDir(), filepath.Join(t.TempDir(), "sess")
	a, b := filepath.Join(work, "a.txt"), filepath.Join(work, "b.txt")
	write(t, a, "a0")

	s := New(dir)
	s.BeginTurn() // turn 1 edits a
	s.Capture(a)
	write(t, a, "a1")
	s.BeginTurn() // turn 2 changes nothing
	s.BeginTurn() // turn 3 creates b
	s.Capture(b)
	write(t, b, "b1")

	// A new run of the same session picks up the saved turns and numbers
	// its own after them, so the empty turn 2 doesn't cause a clash.
	s = New(dir)
	s.BeginTurn()
	s.Capture(a)
	write(t, a, "a2")

	if paths, err := s.Undo(); err != nil || len(paths) != 1 || read(t, a) != "a1" {
		t.Fatalf("undo this run: %v %v, a=%q", paths, err, read(t, a))
	}
	if _, err := s.Undo(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b); !os.IsNotExist(err) {
		t.Fatalf("undoing turn 3 should remove the file it created: %v", err)
	}
	if _, err := s.Undo(); err != nil || read(t, a) != "a0" {
		t.Fatalf("undo turn 1: %v, a=%q", err, read(t, a))
	}
	if _, err := s.Undo(); err == nil {
		t.Fatal("nothing should be left to undo")
	}
}

func TestPrune(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	f := filepath.Join(work, "f.txt")
	write(t, f, "x")
	for _, id := range []string{"old", "new"} {
		s := New(filepath.Join(root, id))
		s.BeginTurn()
		s.Capture(f)
	}
	past := time.Now().Add(-10 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "old", "1"), past, past); err != nil {
		t.Fatal(err)
	}
	if err := Prune(root, 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "old")); !os.IsNotExist(err) {
		t.Fatalf("old session's checkpoints should be gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "new", "1", "manifest.json")); err != nil {
		t.Fatalf("recent checkpoints should stay: %v", err)
	}
}
