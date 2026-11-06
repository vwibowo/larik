package desktop

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"larik/internal/session"
	"larik/internal/status"
	"larik/internal/worktree"
)

func TestChooserClosedDuringWorktreeCreation(t *testing.T) {
	repo := gitRepo(t)
	a := New(Options{Cwd: repo, DataDir: t.TempDir()})
	defer a.Close()
	updates := make(chan func(), 1)
	a.update = func(fn func()) { updates <- fn }
	a.openNew()
	chooser := a.focused()
	a.choose(chooser, 1)
	var finish func()
	select {
	case finish = <-updates: // Git created the worktree; hold its UI completion.
	case <-time.After(5 * time.Second):
		t.Fatal("worktree creation did not finish")
	}
	if got := gitIn(t, repo, "worktree", "list", "--porcelain"); !strings.Contains(got, "larik/session-") {
		t.Fatalf("worktree was not created: %s", got)
	}
	a.closeTab(chooser)
	a.update = func(fn func()) { fn() }
	finish()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := gitIn(t, repo, "worktree", "list", "--porcelain"); !strings.Contains(got, "larik/session-") {
			if len(a.tabs) != 0 {
				t.Fatalf("canceled chooser started %d tabs", len(a.tabs))
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("canceled chooser left its worktree behind")
}

func TestChooserWorktreeStillStartsWhenOpen(t *testing.T) {
	needTerminal(t)
	repo := gitRepo(t)
	a := New(Options{Bin: fakeLarik(t), Cwd: repo, DataDir: t.TempDir()})
	defer a.Close()
	updates := make(chan func(), 1)
	a.update = func(fn func()) { updates <- fn }
	a.openNew()
	chooser := a.focused()
	a.choose(chooser, 1)
	var finish func()
	select {
	case finish = <-updates:
	case <-time.After(5 * time.Second):
		t.Fatal("worktree creation did not finish")
	}
	a.update = func(fn func()) { fn() }
	finish()
	if len(a.tabs) != 1 || a.tabs[0] == chooser || !a.inWorktree(a.tabs[0].dir) {
		t.Fatalf("worktree chooser did not start its session: %+v", a.tabs)
	}
}

func TestUnusedWorktreeCleanupFailureReportsPath(t *testing.T) {
	repo := gitRepo(t)
	a := New(Options{DataDir: t.TempDir()})
	missing := filepath.Join(t.TempDir(), "missing")
	a.removeUnusedWorktree(&worktree.Worktree{Repo: repo, Path: missing, Branch: "larik/missing"})
	if !strings.Contains(a.notice, missing) || !strings.Contains(a.notice, "Couldn't remove unused worktree") {
		t.Fatalf("cleanup failure was not reported: %q", a.notice)
	}
}

// gitRepo is a repository with one commit, and the user's git config
// kept out.
func gitRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	gitIn(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644)
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-qm", "init")
	return repo
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestWorktreeSessions(t *testing.T) {
	needTerminal(t)
	repo, data := gitRepo(t), t.TempDir()
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data, Cwd: repo})
	defer a.Close()
	ln, err := listen(data, func(string) {}, a.control)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// open --worktree: a tab in a new worktree, on its own branch.
	res, err := Call(data, Request{Cmd: "open", Cwd: repo, Worktree: true, Branch: "fix"})
	if err != nil {
		t.Fatal(err)
	}
	wt := res.Tab.Dir
	if !a.inWorktree(wt) || filepath.Base(wt) != "fix" || gitIn(t, wt, "branch", "--show-current") != "larik/fix" {
		t.Fatalf("the worktree: %s", wt)
	}
	if _, err := Call(data, Request{Cmd: "open", Cwd: t.TempDir(), Worktree: true}); err == nil || !strings.Contains(err.Error(), "git repository") {
		t.Fatalf("a worktree outside a repository: %v", err)
	}
	// The session in it did something, and left it uncommitted.
	os.WriteFile(filepath.Join(wt, "b.txt"), []byte("two\n"), 0o644)
	s := session.Indexed{Status: status.Report{State: status.Idle}, Subdir: "fix"}
	s.ID, s.Path, s.Cwd, s.Title = "abc1", filepath.Join(t.TempDir(), "s.jsonl"), wt, "Add b"
	mu.Lock()
	a.setProjects([]session.Project{{Root: repo, Name: "repo", Sessions: []session.Indexed{s}}})
	a.git = map[string]gitMeta{repo: {Branch: "main"}, wt: {Branch: "larik/fix"}}
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	mu.Unlock()

	// Busy: it says so, and asks nothing.
	mu.Lock()
	s.Status.State = status.Working
	a.setProjects([]session.Project{{Root: repo, Name: "repo", Sessions: []session.Indexed{s}}})
	a.tabs[0].path = s.Path
	a.askWorktree(s, repo, true)
	if !strings.Contains(a.notice, "is working in the worktree") || a.asked != nil {
		mu.Unlock()
		t.Fatalf("merging a busy worktree: %q", a.notice)
	}
	s.Status.State = status.Idle
	a.setProjects([]session.Project{{Root: repo, Name: "repo", Sessions: []session.Indexed{s}}})
	a.tabs[0].path = s.Path
	a.askWorktree(s, repo, true)
	mu.Unlock()
	frameUntil(t, mu, tt, "the question", func() bool { return a.asked != nil })
	mu.Lock()
	if q := a.asked; q.title != "Merge “larik/fix” into main?" || !strings.Contains(q.msg, "Its uncommitted changes come into main") {
		mu.Unlock()
		t.Fatalf("asked: %+v", q)
	}
	snapshot(t, tt, "worktree-merge.png")
	if err := tt.Click("Merge"); err != nil {
		mu.Unlock()
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	mu.Unlock()
	frameUntil(t, mu, tt, "the merge", func() bool { return strings.HasPrefix(a.notice, "Merged") })
	mu.Lock()
	notice, tabs := a.notice, len(a.tabs)
	mu.Unlock()
	if notice != "Merged larik/fix into main: 1 commit, 1 file." || tabs != 0 {
		t.Fatalf("after the merge: %q, %d tabs", notice, tabs)
	}
	if _, err := os.Stat(filepath.Join(repo, "b.txt")); err != nil {
		t.Fatal("the work didn't come into the checkout")
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatal("the worktree stayed")
	}

	// Its session opens where its work went.
	mu.Lock()
	if tb := a.sessionTab(s.Path); tb == nil || tb.dir != repo {
		mu.Unlock()
		t.Fatalf("reopening the merged worktree's session: %+v", tb)
	}
	for _, tb := range append([]*tab(nil), a.tabs...) {
		a.closeTab(tb)
	}
	mu.Unlock()

	// Remove: what it had is lost, and it says so first.
	res, err = Call(data, Request{Cmd: "open", Cwd: repo, Worktree: true, Background: true})
	if err != nil {
		t.Fatal(err)
	}
	wt = res.Tab.Dir
	if !strings.HasPrefix(filepath.Base(wt), "session-") {
		t.Fatalf("a named-for-it worktree: %s", wt)
	}
	os.WriteFile(filepath.Join(wt, "c.txt"), []byte("three\n"), 0o644)
	gitIn(t, wt, "add", ".")
	gitIn(t, wt, "commit", "-qm", "c")
	s.Cwd, s.Path = wt, filepath.Join(t.TempDir(), "s2.jsonl")
	mu.Lock()
	a.setProjects([]session.Project{{Root: repo, Name: "repo", Sessions: []session.Indexed{s}}})
	a.askWorktree(s, repo, false)
	mu.Unlock()
	frameUntil(t, mu, tt, "the question", func() bool { return a.asked != nil })
	mu.Lock()
	if q := a.asked; !strings.HasPrefix(q.title, "Remove “larik/session-") || !strings.HasPrefix(q.msg, "Its commit that isn't in main is lost.") {
		mu.Unlock()
		t.Fatalf("asked: %+v", q)
	}
	if err := tt.Click("Remove"); err != nil {
		mu.Unlock()
		t.Fatal(err)
	}
	mu.Unlock()
	frameUntil(t, mu, tt, "the removal", func() bool { return strings.HasPrefix(a.notice, "Removed") })
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatal("the worktree stayed")
	}
	if _, err := os.Stat(filepath.Join(repo, "c.txt")); !os.IsNotExist(err) {
		t.Fatal("a removed worktree's work came in")
	}
}

func TestMergeConflictSaysSo(t *testing.T) {
	needTerminal(t)
	repo, data := gitRepo(t), t.TempDir()
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data, Cwd: repo})
	defer a.Close()
	w, err := a.makeWorktree(repo, "edit")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(w.Path, "a.txt"), []byte("theirs\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("ours\n"), 0o644)
	gitIn(t, repo, "commit", "-qam", "ours")
	mu.Lock()
	tb := a.startTab(w.Path, "", "")
	a.finishWorktree(w, "Edit a", true)
	mu.Unlock()
	waitFor(t, mu, "the merge", func() bool { return strings.Contains(a.notice, "conflicts") })
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(a.notice, "larik/edit conflicts with main in a.txt, so nothing was merged") || len(a.tabs) != 1 || a.tabs[0] != tb {
		t.Fatalf("a conflict: %q, %d tabs", a.notice, len(a.tabs))
	}
	if _, err := os.Stat(w.Path); err != nil {
		t.Fatal("the worktree went")
	}
}

func TestWorktreeRemovalWaitsForHostedTabToStop(t *testing.T) {
	needTerminal(t)
	repo, data := gitRepo(t), t.TempDir()
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data, Cwd: repo})
	defer a.Close()
	w, err := a.makeWorktree(repo, "slow-close")
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	mu.Lock()
	tb := a.startTab(w.Path, "", "")
	if tb == nil {
		mu.Unlock()
		t.Fatalf("start tab: %s", a.notice)
	}
	tb.hosted = true
	a.stopHosted = func(string) error {
		close(entered)
		<-release
		return nil
	}
	a.finishWorktree(w, "", false)
	mu.Unlock()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("host close did not start")
	}
	// The host has not acknowledged stopping its process yet.
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(w.Path); err != nil {
		close(release)
		t.Fatalf("worktree removed before the hosted tab stopped: %v", err)
	}
	close(release)
	waitFor(t, mu, "worktree removal", func() bool { return strings.HasPrefix(a.notice, "Removed") })
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree remained after the hosted tab stopped: %v", err)
	}
}
