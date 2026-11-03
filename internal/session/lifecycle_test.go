package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/status"
)

// newSession is a session in dir, for cwd, with a prompt and the files
// larik keeps beside a transcript.
func newSession(t *testing.T, dir, cwd, prompt string) string {
	t.Helper()
	s, err := Create(dir, Meta{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: prompt}}}, nil); err != nil {
		t.Fatal(err)
	}
	s.Close()
	os.MkdirAll(RawDir(s.Path), 0o700)
	os.WriteFile(RawPath(s.Path, "call"), []byte("output"), 0o600)
	return s.Path
}

func TestNames(t *testing.T) {
	dir := t.TempDir()
	path := newSession(t, dir, dir, "Fix the parser")
	if err := SetName(path, "  Parser   fix "); err != nil {
		t.Fatal(err)
	}
	infos, _ := List(dir)
	if len(infos) != 1 || infos[0].Name != "Parser fix" || infos[0].Title != "Fix the parser" {
		t.Fatalf("listed: %+v", infos)
	}
	// A blank name takes it away; taking away none is fine.
	for range 2 {
		if err := SetName(path, " "); err != nil {
			t.Fatal(err)
		}
	}
	if infos, _ := List(dir); infos[0].Name != "" {
		t.Fatalf("unnamed: %+v", infos[0])
	}
}

func TestArchive(t *testing.T) {
	data := t.TempDir()
	cwd := t.TempDir()
	dir := Dir(data, cwd)
	path := newSession(t, dir, cwd, "Old work")
	keep := newSession(t, dir, cwd, "Current work")
	SetName(path, "Old")
	// A status file from a process that ended: not running.
	w := status.NewWriter(path, "test")
	w.Set(status.Report{State: status.Done})
	w.Release()

	to, err := Archive(path)
	if err != nil {
		t.Fatal(err)
	}
	if to != filepath.Join(dir, "archive", filepath.Base(path)) || !IsArchived(to) {
		t.Fatalf("archived to %s", to)
	}
	for _, p := range []string{to, NamePath(to), status.Path(to), RawPath(to, "call")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s didn't move: %v", filepath.Base(p), err)
		}
	}
	if _, err := os.Stat(RawDir(path)); err == nil {
		t.Error("the raw folder stayed")
	}
	infos, _ := List(dir)
	archived, _ := Archived(dir)
	if len(infos) != 1 || infos[0].Path != keep || len(archived) != 1 || archived[0].Name != "Old" {
		t.Fatalf("listed %+v, archived %+v", infos, archived)
	}
	projects, _ := Index(data, IndexOptions{Scratch: true})
	if len(projects) != 1 || len(projects[0].Sessions) != 1 || len(projects[0].Archived) != 1 || projects[0].Archived[0].Path != to {
		t.Fatalf("indexed: %+v", projects)
	}
	if again, err := Archive(to); err != nil || again != to {
		t.Fatalf("archiving twice: %s, %v", again, err)
	}

	back, err := Unarchive(to)
	if err != nil || back != path {
		t.Fatalf("unarchived to %s: %v", back, err)
	}
	if infos, _ := List(dir); len(infos) != 2 {
		t.Fatalf("back: %d", len(infos))
	}
	if _, err := os.Stat(RawPath(path, "call")); err != nil {
		t.Fatal("the raw output didn't come back")
	}
}

// A session a process has open stays where it is.
func TestArchiveRunning(t *testing.T) {
	dir := t.TempDir()
	path := newSession(t, dir, dir, "Busy")
	w := status.NewWriter(path, "test") // this process: running
	w.Set(status.Report{State: status.Working})
	defer w.Release()
	if _, err := Archive(path); err != ErrRunning {
		t.Fatalf("archived a running session: %v", err)
	}
	if _, err := Gather(path); err != ErrRunning {
		t.Fatalf("gathered a running session: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 3 { // transcript, raw, status
		t.Fatalf("left behind: %v", entries)
	}
}

func TestGather(t *testing.T) {
	dir := t.TempDir()
	path := newSession(t, dir, dir, "Throwaway")
	folder, err := Gather(path)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(folder) != "larik session "+filepath.Base(path[:len(path)-len(".jsonl")]) {
		t.Fatalf("folder %s", folder)
	}
	if infos, _ := List(dir); len(infos) != 0 {
		t.Fatalf("still listed: %+v", infos)
	}
	if _, err := os.Stat(filepath.Join(folder, filepath.Base(path))); err != nil {
		t.Fatal(err)
	}
	// The Trash refused it: it goes back.
	if err := Ungather(path, folder); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(folder); err == nil {
		t.Fatal("the folder stayed")
	}
	if _, err := os.Stat(RawPath(path, "call")); err != nil {
		t.Fatal(err)
	}
}

// Resuming an archived session by its id brings it back.
func TestResumeArchived(t *testing.T) {
	dir := t.TempDir()
	path := newSession(t, dir, "/work", "Old work")
	archived, err := Archive(path)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	got, err := Resume(dir, id[:len(id)-2])
	if err != nil || got != path || IsArchived(got) {
		t.Fatalf("resumed %q, %v", got, err)
	}
	if _, err := os.Stat(archived); err == nil {
		t.Fatal("still in the archive")
	}
	if _, err := Resume(dir, "nope"); err == nil || !strings.Contains(err.Error(), "no session") {
		t.Fatalf("unknown: %v", err)
	}
}
