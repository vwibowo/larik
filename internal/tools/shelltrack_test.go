package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"larik/internal/checkpoint"
)

// trackedRepo is a git repository with files in every state a command can
// find them in, and an Env whose shell changes are recorded for undo.
func trackedRepo(t *testing.T) (*Env, *checkpoint.Store, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git needed")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	put := func(name, content string, mode os.FileMode) {
		t.Helper()
		os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	put(".gitignore", "build/\n", 0o644)
	put("clean.txt", "committed\n", 0o644)
	put("run.sh", "#!/bin/sh\necho hi\n", 0o755)
	put("pkg/x.go", "package pkg\n", 0o644)
	put("edited.txt", "committed\n", 0o644)
	git("add", "-A")
	git("commit", "-q", "-m", "first")
	put("edited.txt", "my uncommitted work\n", 0o644) // modified before the command
	put("notes.txt", "untracked notes\n", 0o644)      // untracked before the command
	put("build/out", "old build\n", 0o644)            // ignored

	env := NewEnv(dir)
	store := checkpoint.New(filepath.Join(t.TempDir(), "ckpt"), dir)
	env.BeforeWrite, env.RecordOriginal = store.Capture, store.Record
	store.BeginTurn()
	return env, store, dir
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "<missing>"
	}
	return string(data)
}

func TestUndoRestoresWhatAShellCommandChanged(t *testing.T) {
	env, store, dir := trackedRepo(t)
	script := `echo overwritten > clean.txt
rm run.sh
echo more >> edited.txt
rm notes.txt
echo brand new > created.txt
mkdir -p deep/er && echo nested > deep/er/new.txt
rm -rf pkg
echo rebuilt > build/out`
	if r := run(t, Bash{}, env, `{"command":`+quote(script)+`,"sandbox":false}`); r.IsError {
		t.Fatal(r.Content)
	}
	if read(t, dir, "clean.txt") != "overwritten\n" || read(t, dir, "pkg/x.go") != "<missing>" {
		t.Fatal("the command should have changed the files")
	}

	restored, err := store.Undo()
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"clean.txt":       "committed\n",           // was clean: back to the commit
		"run.sh":          "#!/bin/sh\necho hi\n",  // deleted: back
		"edited.txt":      "my uncommitted work\n", // was already modified: back to that, not to the commit
		"notes.txt":       "untracked notes\n",     // untracked and deleted: back
		"created.txt":     "<missing>",             // created by the command: gone
		"deep/er/new.txt": "<missing>",
		"pkg/x.go":        "package pkg\n", // its directory was removed too
		"build/out":       "rebuilt\n",     // ignored by git: not covered
	} {
		if got := read(t, dir, name); got != want {
			t.Errorf("%s after undo = %q, want %q", name, got, want)
		}
	}
	if fi, err := os.Stat(filepath.Join(dir, "run.sh")); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Error("run.sh should be executable again")
	}
	var names []string
	for _, p := range restored {
		rel, _ := filepath.Rel(dir, p)
		names = append(names, filepath.ToSlash(rel))
	}
	slices.Sort(names)
	if got := strings.Join(names, ","); got != "clean.txt,created.txt,deep/er/new.txt,edited.txt,notes.txt,pkg/x.go,run.sh" {
		t.Errorf("restored: %s", got)
	}
}

func TestShellTrackingLeavesTheRestAlone(t *testing.T) {
	env, store, dir := trackedRepo(t)

	// A command that changes nothing leaves nothing to undo.
	if r := run(t, Bash{}, env, `{"command":"cat clean.txt && ls","sandbox":false}`); r.IsError {
		t.Fatal(r.Content)
	}
	if _, err := store.Undo(); err == nil {
		t.Error("a read-only command should leave nothing to undo")
	}

	// Committing changes no file's content, so there is nothing to undo
	// either (the commit itself is git's to undo).
	store.BeginTurn()
	if r := run(t, Bash{}, env, `{"command":"git -c user.name=t -c user.email=t@example.com -c commit.gpgsign=false commit -qam work","sandbox":false}`); r.IsError {
		t.Fatal(r.Content)
	}
	if _, err := store.Undo(); err == nil {
		t.Error("a commit leaves the files as they were")
	}
	if read(t, dir, "edited.txt") != "my uncommitted work\n" {
		t.Error("the committed file should be untouched")
	}

	// A file the edit tool changed first keeps that older original.
	store.BeginTurn()
	run(t, Read{}, env, `{"path":"clean.txt"}`)
	if r := run(t, Edit{}, env, `{"path":"clean.txt","old_string":"committed","new_string":"edited by the tool"}`); r.IsError {
		t.Fatal(r.Content)
	}
	if r := run(t, Bash{}, env, `{"command":"echo then the shell > clean.txt","sandbox":false}`); r.IsError {
		t.Fatal(r.Content)
	}
	if _, err := store.Undo(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, dir, "clean.txt"); got != "committed\n" {
		t.Errorf("undo should go back to before the turn, got %q", got)
	}
}

func TestShellTrackingOutsideARepository(t *testing.T) {
	dir := t.TempDir()
	env := NewEnv(dir)
	store := checkpoint.New(filepath.Join(t.TempDir(), "ckpt"), dir)
	env.BeforeWrite, env.RecordOriginal = store.Capture, store.Record
	store.BeginTurn()
	if r := run(t, Bash{}, env, `{"command":"echo x > file.txt","sandbox":false}`); r.IsError {
		t.Fatalf("a command outside git should just run: %s", r.Content)
	}
	if _, err := store.Undo(); err == nil {
		t.Error("outside a repository shell changes aren't tracked")
	}
	// Without a checkpoint store (a worktree subagent) nothing is tracked.
	if NewEnv(dir).trackShell(t.Context()) != nil {
		t.Error("no tracking without somewhere to record")
	}
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

// A project opened through a symlink is still tracked: git reports resolved
// paths, the session knows the project by the link.
func TestShellTrackingThroughASymlink(t *testing.T) {
	_, _, dir := trackedRepo(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skip("no symlinks:", err)
	}
	env := NewEnv(link)
	store := checkpoint.New(filepath.Join(t.TempDir(), "ckpt"), link)
	env.BeforeWrite, env.RecordOriginal = store.Capture, store.Record
	store.BeginTurn()
	if r := run(t, Bash{}, env, `{"command":"echo overwritten > clean.txt","sandbox":false}`); r.IsError {
		t.Fatal(r.Content)
	}
	if _, err := store.Undo(); err != nil {
		t.Fatalf("undo through a symlinked project: %v", err)
	}
	if got := read(t, dir, "clean.txt"); got != "committed\n" {
		t.Errorf("clean.txt after undo = %q", got)
	}
}
