package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// isolate keeps the user's git config out of the tests.
func isolate(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T) string {
	t.Helper()
	isolate(t)
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	run(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644)
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-qm", "init")
	return repo
}

func TestKeepsChanges(t *testing.T) {
	repo := newRepo(t)
	root := t.TempDir()
	ctx := context.Background()
	w, err := Create(ctx, root, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(w.Path, Dir(root, repo)) || !strings.HasPrefix(w.Branch, BranchPrefix) {
		t.Errorf("worktree: %+v", w)
	}
	if cd := w.CommonDir(ctx); cd != filepath.Join(repo, ".git") {
		t.Errorf("common dir: %s", cd)
	}
	// One commit by the agent, plus uncommitted leftovers.
	os.WriteFile(filepath.Join(w.Path, "a.txt"), []byte("two\n"), 0o644)
	run(t, w.Path, "commit", "-qam", "edit a")
	os.WriteFile(filepath.Join(w.Path, "b.txt"), []byte("new\n"), 0o644)

	// Uncommitted changes in the main checkout are untouched.
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("user edit\n"), 0o644)

	out, err := w.Finish("larik: test task")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Kept || !out.Committed || out.Commits != 2 || strings.Join(out.Files, ",") != "a.txt,b.txt" {
		t.Errorf("outcome: %+v", out)
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(got) != "user edit\n" {
		t.Errorf("main checkout changed: %q", got)
	}
	if show := run(t, repo, "show", w.Branch+":b.txt"); show != "new" {
		t.Errorf("branch content: %q", show)
	}
	rep := w.Report(out)
	for _, want := range []string{w.Branch, "git merge " + w.Branch, "a.txt, b.txt", "NOT in your working tree"} {
		if !strings.Contains(rep, want) {
			t.Errorf("report missing %q:\n%s", want, rep)
		}
	}

	list, err := List(ctx, repo)
	if err != nil || len(list) != 1 || list[0].Branch != w.Branch || list[0].Commits != 2 || list[0].Dirty {
		t.Fatalf("list: %+v %v", list, err)
	}
	o, err := Open(ctx, repo, w.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if list, _ := List(ctx, repo); len(list) != 0 {
		t.Errorf("after remove: %+v", list)
	}
	if _, err := os.Stat(filepath.Dir(w.Path)); !os.IsNotExist(err) {
		t.Error("worktree directory left behind")
	}
}

func TestRemovesWhenUnchanged(t *testing.T) {
	repo := newRepo(t)
	w, err := Create(context.Background(), t.TempDir(), repo)
	if err != nil {
		t.Fatal(err)
	}
	out, err := w.Finish("nothing")
	if err != nil || out.Kept {
		t.Fatalf("outcome: %+v %v", out, err)
	}
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Error("worktree not removed")
	}
	if branches := run(t, repo, "branch", "--list", BranchPrefix+"*"); branches != "" {
		t.Errorf("branch left: %q", branches)
	}
	if !strings.Contains(w.Report(out), "no changes") {
		t.Error(w.Report(out))
	}
}

func TestParallelCreate(t *testing.T) {
	repo := newRepo(t)
	root := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w, err := Create(context.Background(), root, repo)
			if err == nil {
				os.WriteFile(filepath.Join(w.Path, w.Name+".txt"), []byte("x"), 0o644)
				_, err = w.Finish("parallel")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if list, _ := List(context.Background(), repo); len(list) != 6 {
		t.Errorf("want 6 kept worktrees, got %d", len(list))
	}
}

func TestNeedsACommit(t *testing.T) {
	isolate(t)
	repo := t.TempDir()
	run(t, repo, "init", "-q")
	if _, err := Create(context.Background(), t.TempDir(), repo); err == nil || !strings.Contains(err.Error(), "at least one commit") {
		t.Errorf("empty repo: %v", err)
	}
}
