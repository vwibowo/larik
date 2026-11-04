package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteChangesActionsStayOnHost(t *testing.T) {
	needTerminal(t)
	repo := gitRepo(t)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, connect := standIn(t, t.TempDir())
	dial, _, stop, _, err := connect("devbox")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	source := remoteProject{name: "devbox", dial: dial}
	call := func(req remoteProjectRequest) remoteProjectReply {
		t.Helper()
		req.Dir = remotePath("devbox", repo)
		reply, err := source.call(req)
		if err != nil {
			t.Fatalf("%s: %v", req.Op, err)
		}
		return reply
	}
	read := call(remoteProjectRequest{Op: "read"}).Changes
	if read == nil || read.Root != repo || len(read.Files) != 1 || read.Files[0].New != "one\ntwo\n" {
		t.Fatalf("remote read: %+v", read)
	}
	if got := changesLocal(read, "devbox", compareCommit).root; got != remotePath("devbox", repo) {
		t.Fatalf("client root: %q", got)
	}
	call(remoteProjectRequest{Op: "stage", Path: "a.txt"})
	if got := gitIn(t, repo, "diff", "--cached", "--name-only"); got != "a.txt" {
		t.Fatalf("stage on host: %q", got)
	}
	call(remoteProjectRequest{Op: "unstage", Path: "a.txt"})
	if got := gitIn(t, repo, "diff", "--cached", "--name-only"); got != "" {
		t.Fatalf("unstage on host: %q", got)
	}
	if got := call(remoteProjectRequest{Op: "commit", Message: "remote change", All: true}).Text; !strings.HasPrefix(got, "Committed ") {
		t.Fatalf("commit on host: %q", got)
	}
	if got := gitIn(t, repo, "log", "-1", "--format=%s"); got != "remote change" {
		t.Fatalf("remote commit: %q", got)
	}
	if _, err := source.call(remoteProjectRequest{Op: "stage", Dir: remotePath("devbox", repo), Path: "../outside"}); err == nil {
		t.Fatal("stage accepted a path outside the repository")
	}
}

func TestRemoteWorktreeLifecycle(t *testing.T) {
	needTerminal(t)
	repo, data := gitRepo(t), t.TempDir()
	_, connect := standIn(t, data)
	dial, _, stop, _, err := connect("devbox")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	source := remoteProject{name: "devbox", dial: dial}
	call := func(req remoteProjectRequest) remoteProjectReply {
		t.Helper()
		req.Dir = remotePath("devbox", repo)
		reply, err := source.call(req)
		if err != nil {
			t.Fatalf("%s: %v", req.Op, err)
		}
		return reply
	}
	w := call(remoteProjectRequest{Op: "worktree-create", Name: "feature"}).Worktree
	if w == nil || !strings.HasPrefix(w.Path, "ssh://devbox/") {
		t.Fatalf("remote worktree: %+v", w)
	}
	_, path, _ := splitRemote(w.Path)
	if got := gitIn(t, path, "branch", "--show-current"); got != "larik/feature" {
		t.Fatalf("branch: %q", got)
	}
	if err := os.WriteFile(filepath.Join(path, "b.txt"), []byte("remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := call(remoteProjectRequest{Op: "worktree-info", Worktree: w.Path}).Info; got == nil || !got.Dirty {
		t.Fatalf("worktree info: %+v", got)
	}
	if got := call(remoteProjectRequest{Op: "worktree-merge", Worktree: w.Path, Message: "remote feature"}).Merged; got == nil || !got.Committed {
		t.Fatalf("merge result: %+v", got)
	}
	call(remoteProjectRequest{Op: "worktree-remove", Worktree: w.Path})
	if _, err := os.Stat(filepath.Join(repo, "b.txt")); err != nil {
		t.Fatalf("merged file missing: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
	a := New(Options{DataDir: t.TempDir()})
	defer a.Close()
	a.servers = []*server{{name: "devbox", worktreeRoot: filepath.Join(data, "worktrees")}}
	if !a.inWorktree(w.Path) {
		t.Fatal("remote worktree path was not recognized")
	}
}
