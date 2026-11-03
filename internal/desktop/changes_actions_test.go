package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestChangesActions(t *testing.T) {
	repo := gitRepo(t)
	write := func(name, text string) { os.WriteFile(filepath.Join(repo, name), []byte(text), 0o644) }
	write("d.txt", "keep\n")
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-qm", "d")
	remote := t.TempDir()
	gitIn(t, remote, "init", "-q", "--bare")
	gitIn(t, repo, "remote", "add", "origin", remote)
	gitIn(t, repo, "checkout", "-qb", "feature")
	write("f.txt", "feature\n")
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-qm", "f")
	// What's uncommitted: a change, a new file, a deletion.
	write("a.txt", "one\ntwo\n")
	write("n.txt", "new\n")
	os.Remove(filepath.Join(repo, "d.txt"))
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	// A gh that says what it was asked, and has a pull request once told.
	bin, said := t.TempDir(), filepath.Join(t.TempDir(), "gh.log")
	os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\necho \"$*\" >> "+said+"\ncase \"$1 $2\" in \"pr view\") [ -f "+said+".pr ] && echo https://github.com/o/r/pull/7 && exit 0; exit 1;; esac\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	a, mu := serialApp(Options{})
	var opened string
	a.openExternal = func(url string) { opened = url }
	ch := &changes{dir: repo, stop: make(chan struct{}), kick: make(chan struct{}, 1)}
	read := func() {
		t.Helper()
		r := readChanges(repo, headCache{}, ch.against)
		mu.Lock()
		ch.apply(r)
		mu.Unlock()
	}
	done := func(what string) {
		t.Helper()
		waitFor(t, mu, what, func() bool { return ch.busy == "" })
		read()
	}
	codes := func() map[string]string {
		m := map[string]string{}
		for _, f := range ch.files {
			m[f.path] = f.code
		}
		return m
	}
	read()
	if ch.branch != "feature" || ch.base != "main" || !ch.remote || !ch.canPullRequest() || len(ch.files) != 3 {
		t.Fatalf("read: branch %q base %q remote %v, %v", ch.branch, ch.base, ch.remote, codes())
	}
	pane := ui.NewTester(func(c *ui.Context) { a.changesPane(c, a.palette(ui.DarkTheme()), ch) }, 640, 800)
	pane.SetDark(true)
	mu.Lock()
	pane.Frame()
	for _, want := range []string{"Uncommitted", "Since main", "Commit All", "Stage All", "Pull Request"} {
		if !pane.HasText(want) {
			mu.Unlock()
			t.Fatalf("no %q in %q", want, pane.Texts())
		}
	}
	snapshot(t, pane, "changes-actions-dark.png")
	// Stage a.txt with its box: one staged, so Commit commits it alone.
	if err := pane.Click("Stage a.txt"); err != nil {
		mu.Unlock()
		t.Fatal(err)
	}
	mu.Unlock()
	done("staging")
	if c := codes()["a.txt"]; c != "M " {
		t.Fatalf("staged: %q", c)
	}
	mu.Lock()
	ch.message = "Two lines"
	pane.Frame()
	if err := pane.Click("Commit"); err != nil {
		mu.Unlock()
		t.Fatalf("%v: %q", err, pane.Texts())
	}
	mu.Unlock()
	done("the commit")
	mu.Lock()
	if _, ok := codes()["a.txt"]; ok || len(ch.files) != 2 || !strings.HasPrefix(a.notice, "Committed ") || !strings.HasSuffix(a.notice, " Two lines.") || ch.message != "" {
		mu.Unlock()
		t.Fatalf("after the commit: %v %q %q", codes(), a.notice, ch.message)
	}
	// Discarding asks first: the new file goes, the deleted one comes back.
	for _, name := range []string{"n.txt", "d.txt"} {
		for _, f := range ch.files {
			if f.path == name {
				a.askDiscard(ch, f)
			}
		}
		q := a.asked
		if q == nil || q.yes != "Discard" {
			mu.Unlock()
			t.Fatalf("discarding %s asks: %+v", name, q)
		}
		if name == "n.txt" && q.title != "Delete “n.txt”?" {
			t.Errorf("an untracked file: %q", q.title)
		}
		a.asked = nil
		q.then()
		mu.Unlock()
		done("discarding " + name)
		mu.Lock()
	}
	mu.Unlock()
	if len(ch.files) != 0 {
		t.Fatalf("after discarding: %v", codes())
	}
	if _, err := os.Stat(filepath.Join(repo, "n.txt")); !os.IsNotExist(err) {
		t.Fatal("n.txt stayed")
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "d.txt")); string(b) != "keep\n" {
		t.Fatalf("d.txt: %q", b)
	}
	// Since main: both of the branch's commits.
	mu.Lock()
	ch.compare(compareBase)
	mu.Unlock()
	read()
	if c := codes(); len(c) != 2 || c["f.txt"] != "A " || c["a.txt"] != "M " {
		t.Fatalf("since main: %v", c)
	}
	// No pull request yet: it asks, then pushes and opens one.
	mu.Lock()
	a.askPullRequest(ch)
	mu.Unlock()
	waitFor(t, mu, "the question", func() bool { return a.asked != nil })
	mu.Lock()
	q := a.asked
	a.asked = nil
	if q.title != "Open a pull request for “feature”?" || !strings.Contains(q.msg, "into main") {
		mu.Unlock()
		t.Fatalf("asked: %+v", q)
	}
	q.then()
	mu.Unlock()
	done("the push")
	if a.notice != "Pushed feature: finish the pull request in the browser." {
		t.Fatalf("after the push: %q", a.notice)
	}
	if gitIn(t, remote, "rev-parse", "feature") != gitIn(t, repo, "rev-parse", "HEAD") {
		t.Fatal("the branch wasn't pushed")
	}
	if log, _ := os.ReadFile(said); !strings.Contains(string(log), "pr create --web --head feature --base main") {
		t.Fatalf("gh: %s", log)
	}
	// One there: it opens.
	os.WriteFile(said+".pr", nil, 0o644)
	mu.Lock()
	a.askPullRequest(ch)
	mu.Unlock()
	done("the pull request")
	if opened != "https://github.com/o/r/pull/7" || a.asked != nil {
		t.Fatalf("opened %q, asked %+v", opened, a.asked)
	}
}
