package desktop

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestChangesFingerprintCatchesLocalEdits(t *testing.T) {
	repo := gitRepo(t)
	file := filepath.Join(repo, "tracked.txt")
	write := func(text string, stamp int64) {
		t.Helper()
		if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		at := time.Unix(stamp, 0)
		if err := os.Chtimes(file, at, at); err != nil {
			t.Fatal(err)
		}
	}
	write("original\n", 1)
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-qm", "initial")
	root, indexPath := changesRepo(repo)
	if root != repo || indexPath == "" {
		t.Fatalf("repository paths: %q, %q", root, indexPath)
	}
	fingerprint := func() string {
		t.Helper()
		got, err := changesFingerprint(context.Background(), root, indexPath)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	previous := fingerprint()
	changed := func(what string) {
		t.Helper()
		next := fingerprint()
		if next == previous {
			t.Errorf("%s did not change the fingerprint", what)
		}
		previous = next
	}
	write("first edit\n", 2)
	changed("working tree edit")
	write("second edit\n", 3)
	changed("another edit with the same Git status")
	gitIn(t, repo, "add", "tracked.txt")
	changed("staging")
	write("third edit\n", 4)
	gitIn(t, repo, "add", "tracked.txt")
	changed("new staged content with the same Git status")
	untracked := filepath.Join(repo, "new.txt")
	if err := os.WriteFile(untracked, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed("untracked file")
	if err := os.WriteFile(untracked, []byte("two longer"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed("untracked content")
	gitIn(t, repo, "checkout", "-qb", "other")
	changed("branch switch at the same commit")
	if got := fingerprint(); got != previous {
		t.Fatal("unchanged repository produced a different fingerprint")
	}
}

func TestChangesWatcherSkipsIdleAndRefreshesOnEditOrAction(t *testing.T) {
	repo := gitRepo(t)
	file := filepath.Join(repo, "tracked.txt")
	if err := os.WriteFile(file, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-qm", "initial")
	if err := os.WriteFile(file, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(Options{})
	updates := make(chan struct{}, 4)
	a.update = func(fn func()) {
		fn()
		updates <- struct{}{}
	}
	ch := &changes{dir: repo, stop: make(chan struct{}), kick: make(chan struct{}, 1)}
	done := make(chan struct{})
	go func() { a.watchChanges(ch); close(done) }()
	defer func() {
		close(ch.stop)
		<-done
		a.Close()
	}()
	waitUpdate := func(what string, timeout time.Duration) {
		t.Helper()
		select {
		case <-updates:
		case <-time.After(timeout):
			t.Fatalf("no Changes refresh after %s", what)
		}
	}
	waitUpdate("opening", 3*time.Second)
	select {
	case <-updates:
		t.Fatal("unchanged Changes pane made another full read")
	case <-time.After(2500 * time.Millisecond):
	}
	if err := os.WriteFile(file, []byte("second edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitUpdate("file edit", 3*time.Second)
	if len(ch.files) != 1 || len(ch.files[0].parts) == 0 || ch.files[0].parts[0].new != "second edit\n" {
		t.Fatalf("file edit was not shown: %+v", ch.files)
	}
	ch.reread()
	waitUpdate("explicit reread", time.Second)
}
