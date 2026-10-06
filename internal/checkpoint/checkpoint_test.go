package checkpoint

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"larik/internal/session"
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

	s := New(dir, work)
	s.BeginTurn() // turn 1 edits a
	s.Capture(a)
	write(t, a, "a1")
	s.BeginTurn() // turn 2 changes nothing
	s.BeginTurn() // turn 3 creates b
	s.Capture(b)
	write(t, b, "b1")

	// A new run of the same session picks up the saved turns and numbers
	// its own after them, so the empty turn 2 doesn't cause a clash.
	s = New(dir, work)
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
		s := New(filepath.Join(root, id), work)
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

func TestCaptureAndUndoCanRetryAfterFailure(t *testing.T) {
	work := t.TempDir()
	path := filepath.Join(work, "file.txt")
	write(t, path, "before")
	blocker := filepath.Join(t.TempDir(), "blocker")
	write(t, blocker, "not a directory")
	s := New(filepath.Join(blocker, "checkpoints"), work)
	s.BeginTurn()
	if err := s.Capture(path); err == nil {
		t.Fatal("capture should fail when checkpoint directory is inaccessible")
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := s.Capture(path); err != nil {
		t.Fatalf("capture retry failed: %v", err)
	}
	write(t, path, "after")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(blocker, "checkpoints", "1", "0.bin")
	missing := blob + ".missing"
	if err := os.Rename(blob, missing); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Undo(); err == nil {
		t.Fatal("undo should fail when a snapshot blob is missing")
	}
	if err := os.Rename(missing, blob); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Undo(); err != nil || read(t, path) != "before" {
		t.Fatalf("undo retry: %v, content %q", err, read(t, path))
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("undo did not restore mode: %v, %v", info, err)
	}
}

func TestUndoRejectsReplacedSymlink(t *testing.T) {
	work, outside := t.TempDir(), t.TempDir()
	path := filepath.Join(work, "file.txt")
	external := filepath.Join(outside, "external.txt")
	write(t, path, "before")
	write(t, external, "external")
	s := New(filepath.Join(t.TempDir(), "checkpoints"), work)
	s.BeginTurn()
	if err := s.Capture(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Undo(); err == nil {
		t.Fatal("undo followed a replaced symlink")
	}
	if got := read(t, external); got != "external" {
		t.Fatalf("outside file changed: %q", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	write(t, path, "after")
	if _, err := s.Undo(); err != nil || read(t, path) != "before" {
		t.Fatalf("undo retry: %v", err)
	}
}

func TestUndoRejectsReplacedParentDirectory(t *testing.T) {
	work, outside := t.TempDir(), t.TempDir()
	dir := filepath.Join(work, "sub")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "file.txt")
	external := filepath.Join(outside, "file.txt")
	write(t, path, "before")
	write(t, external, "external")
	s := New(filepath.Join(t.TempDir(), "checkpoints"), work)
	s.BeginTurn()
	if err := s.Capture(path); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(work, "moved")
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Undo(); err == nil {
		t.Fatal("undo followed a replaced parent directory")
	}
	if got := read(t, external); got != "external" {
		t.Fatalf("outside file changed: %q", got)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, dir); err != nil {
		t.Fatal(err)
	}
	write(t, path, "after")
	if _, err := s.Undo(); err != nil || read(t, path) != "before" {
		t.Fatalf("undo retry: %v", err)
	}
}

func TestManifestSaveFailureLeavesNoTemporaryFile(t *testing.T) {
	work := t.TempDir()
	path := filepath.Join(work, "file.txt")
	write(t, path, "before")
	checkpoints := filepath.Join(t.TempDir(), "checkpoints")
	tdir := filepath.Join(checkpoints, "1")
	if err := os.MkdirAll(filepath.Join(tdir, "manifest.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	s := New(checkpoints, work)
	s.BeginTurn()
	if err := s.Capture(path); err == nil {
		t.Fatal("manifest rename should fail when destination is a directory")
	}
	entries, err := os.ReadDir(tdir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if len(entry.Name()) >= len(".manifest-") && entry.Name()[:len(".manifest-")] == ".manifest-" {
			t.Fatalf("temporary manifest remained: %s", entry.Name())
		}
	}
	if err := os.Remove(filepath.Join(tdir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if err := s.Capture(path); err != nil {
		t.Fatalf("capture retry after manifest failure: %v", err)
	}
}

func TestResumeUndoInSecondProcess(t *testing.T) {
	work, sessions, checkpoints := t.TempDir(), t.TempDir(), t.TempDir()
	path := filepath.Join(work, "file.txt")
	write(t, path, "before")
	sess, err := session.Create(sessions, session.Meta{Cwd: work, Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	store := New(checkpoints, work)
	store.BeginTurn()
	if err := store.Capture(path); err != nil {
		t.Fatal(err)
	}
	write(t, path, "after")
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestResumeUndoHelper$")
	cmd.Env = append(os.Environ(), "LARIK_RESUME_UNDO_SESSION="+sess.Path, "LARIK_RESUME_UNDO_CHECKPOINTS="+checkpoints, "LARIK_RESUME_UNDO_WORK="+work)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("resumed process: %v: %s", err, out)
	}
	if got := read(t, path); got != "before" {
		t.Fatalf("second process did not restore file: %q", got)
	}
	if _, err := New(checkpoints, work).Undo(); err == nil {
		t.Fatal("resumed undo left a stale snapshot")
	}
}

func TestResumeUndoHelper(t *testing.T) {
	path := os.Getenv("LARIK_RESUME_UNDO_SESSION")
	if path == "" {
		return
	}
	sess, _, err := session.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	store := New(os.Getenv("LARIK_RESUME_UNDO_CHECKPOINTS"), os.Getenv("LARIK_RESUME_UNDO_WORK"))
	if _, err := store.Undo(); err != nil {
		t.Fatal(err)
	}
}

// turnAt changes path in a new turn that starts changing files at a known
// time, standing in for a prompt sent then.
func turnAt(t *testing.T, s *Store, path, content string) time.Time {
	t.Helper()
	time.Sleep(5 * time.Millisecond) // keep turns apart on coarse clocks
	s.BeginTurn()
	if err := s.Capture(path); err != nil {
		t.Fatal(err)
	}
	write(t, path, content)
	return s.turns[len(s.turns)-1].Time
}

func TestUndoSinceRevertsEveryLaterTurn(t *testing.T) {
	work, dir := t.TempDir(), filepath.Join(t.TempDir(), "sess")
	a, b := filepath.Join(work, "a.txt"), filepath.Join(work, "b.txt")
	write(t, a, "a0")
	s := New(dir, work)

	turnAt(t, s, a, "a1")       // prompt 1
	t2 := turnAt(t, s, a, "a2") // prompt 2 edits a again...
	s.Capture(b)                // ...and creates b
	write(t, b, "b2")
	turnAt(t, s, a, "a3") // prompt 3
	s.BeginTurn()         // prompt 4 changes nothing

	want := map[string]bool{a: true, b: true}
	got := s.Since(t2)
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Fatalf("Since(prompt 2) = %v", got)
	}
	if read(t, a) != "a3" {
		t.Fatal("Since must not change anything")
	}

	paths, err := s.UndoSince(t2)
	if err != nil || len(paths) != 2 {
		t.Fatalf("UndoSince: %v %v", paths, err)
	}
	if read(t, a) != "a1" {
		t.Errorf("a = %q, want its state before prompt 2 (a1)", read(t, a))
	}
	if _, err := os.Stat(b); !os.IsNotExist(err) {
		t.Error("b was created after the cut and must be removed")
	}
	// Prompt 1's change survives and is still undoable on its own.
	if paths, err := s.Undo(); err != nil || len(paths) != 1 || read(t, a) != "a0" {
		t.Fatalf("turn 1 should remain: %v %v a=%q", paths, err, read(t, a))
	}
}

func TestUndoSinceKeepsEarlierWorkAndSurvivesResume(t *testing.T) {
	work, dir := t.TempDir(), filepath.Join(t.TempDir(), "sess")
	a := filepath.Join(work, "a.txt")
	write(t, a, "a0")
	s := New(dir, work)
	t1 := turnAt(t, s, a, "a1")
	t2 := turnAt(t, s, a, "a2")

	if got := s.Since(t2.Add(time.Millisecond)); len(got) != 0 {
		t.Errorf("nothing changed after the last turn: %v", got)
	}
	// A later run of the session loads the same turns with their times.
	s = New(dir, work)
	if got := s.Since(t2); len(got) != 1 {
		t.Fatalf("resumed Since = %v", got)
	}
	if _, err := s.UndoSince(t2); err != nil || read(t, a) != "a1" {
		t.Fatalf("resumed UndoSince: %v a=%q", err, read(t, a))
	}
	if got := s.Since(t1); len(got) != 1 {
		t.Errorf("turn 1 is still there: %v", got)
	}
}

func TestOldManifestsWithoutTimesFallBackToTheirFileTime(t *testing.T) {
	work, dir := t.TempDir(), filepath.Join(t.TempDir(), "sess")
	a := filepath.Join(work, "a.txt")
	write(t, a, "a0")
	s := New(dir, work)
	turnAt(t, s, a, "a1")
	manifest := filepath.Join(dir, "1", "manifest.json")
	data := read(t, manifest)
	// Rewrite the manifest the way earlier releases saved it: no time.
	var m map[string]any
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "time")
	b, _ := json.Marshal(m)
	write(t, manifest, string(b))

	s = New(dir, work)
	if got := s.Since(time.Now().Add(-time.Minute)); len(got) != 1 {
		t.Errorf("an untimed manifest should be dated by its file: %v", got)
	}
	if got := s.Since(time.Now().Add(time.Minute)); len(got) != 0 {
		t.Errorf("and not later than that: %v", got)
	}
}
