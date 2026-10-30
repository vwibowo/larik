package checkpoint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func changeFixture(t *testing.T) (*Store, string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(t.TempDir(), root), root, filepath.Join(root, "a.txt")
}

func edit(t *testing.T, s *Store, path, content string) {
	t.Helper()
	s.BeginTurn()
	if err := s.Capture(path); err != nil {
		t.Fatal(err)
	}
	write(t, path, content)
}

func TestChangesShowEachTurn(t *testing.T) {
	s, _, path := changeFixture(t)
	write(t, path, "one\n")
	edit(t, s, path, "two\n")
	edit(t, s, path, "three\n")
	sets := s.Changes()
	if len(sets) != 2 {
		t.Fatalf("sets = %+v", sets)
	}
	newest, oldest := sets[0].Changes[0], sets[1].Changes[0]
	if string(newest.Before) != "two\n" || string(newest.After) != "three\n" || !newest.Latest {
		t.Fatalf("newest = %+v", newest)
	}
	if string(oldest.Before) != "one\n" || string(oldest.After) != "two\n" || oldest.Latest {
		t.Fatalf("oldest = %+v", oldest)
	}
}

func TestChangesCoverNewAndDeletedFiles(t *testing.T) {
	s, _, path := changeFixture(t)
	edit(t, s, path, "new\n")
	c := s.Changes()[0].Changes[0]
	if c.Existed || !c.Exists || string(c.After) != "new\n" {
		t.Fatalf("created = %+v", c)
	}
	s.BeginTurn()
	if err := s.Capture(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	c = s.Changes()[0].Changes[0]
	if !c.Existed || c.Exists || !c.Latest {
		t.Fatalf("deleted = %+v", c)
	}
}

func TestChangesSkipFilesThatEndedTheSame(t *testing.T) {
	s, _, path := changeFixture(t)
	write(t, path, "same\n")
	edit(t, s, path, "same\n")
	if sets := s.Changes(); len(sets) != 0 {
		t.Fatalf("sets = %+v", sets)
	}
}

func TestChangesMarkFilesThatCannotBeCompared(t *testing.T) {
	s, _, path := changeFixture(t)
	write(t, path, "a\x00b")
	edit(t, s, path, "c\x00d")
	if c := s.Changes()[0].Changes[0]; !c.Binary {
		t.Fatalf("binary = %+v", c)
	}
	big := filepath.Join(filepath.Dir(path), "big.txt")
	write(t, big, "x")
	edit(t, s, big, strings.Repeat("y", maxChangeBytes+1))
	for _, c := range s.Changes()[0].Changes {
		if c.Path == big && (!c.Large || c.After != nil) {
			t.Fatalf("large = %+v", c)
		}
	}
}

func TestReplaceKeepsUndoWorking(t *testing.T) {
	s, _, path := changeFixture(t)
	write(t, path, "one\n")
	edit(t, s, path, "two\n")
	c := s.Changes()[0].Changes[0]
	if err := s.Replace(c, []byte("partly\n"), false); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "partly\n" {
		t.Fatalf("file = %q", got)
	}
	if _, err := s.Undo(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "one\n" {
		t.Fatalf("after undo = %q", got)
	}
}

func TestReplaceRefusesWhatItCannotDoSafely(t *testing.T) {
	s, _, path := changeFixture(t)
	write(t, path, "one\n")
	edit(t, s, path, "two\n")
	shown := s.Changes()[0].Changes[0]
	edit(t, s, path, "three\n")
	if err := s.Replace(s.Changes()[1].Changes[0], []byte("x"), false); err == nil || !strings.Contains(err.Error(), "later change") {
		t.Fatalf("older change: %v", err)
	}
	shown.Latest = true
	if err := s.Replace(shown, []byte("x"), false); err == nil || !strings.Contains(err.Error(), "changed after") {
		t.Fatalf("stale change: %v", err)
	}
	if got := read(t, path); got != "three\n" {
		t.Fatalf("file = %q", got)
	}
	u := s.Begin("worker")
	defer u.End()
	if err := s.Replace(s.Changes()[0].Changes[0], []byte("x"), false); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("running task: %v", err)
	}
}

func TestReplaceRemovesACreatedFile(t *testing.T) {
	s, _, path := changeFixture(t)
	edit(t, s, path, "new\n")
	if err := s.Replace(s.Changes()[0].Changes[0], nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stat: %v", err)
	}
	if sets := s.Changes(); len(sets) != 0 {
		t.Fatalf("sets = %+v", sets)
	}
}

func TestReplaceBringsBackADeletedFile(t *testing.T) {
	s, _, path := changeFixture(t)
	write(t, path, "keep\n")
	s.BeginTurn()
	if err := s.Capture(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(s.Changes()[0].Changes[0], []byte("keep\n"), false); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "keep\n" {
		t.Fatalf("file = %q", got)
	}
}
