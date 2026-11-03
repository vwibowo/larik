package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"larik/internal/llm"
	"larik/internal/status"
	"larik/internal/worktree"
)

func TestIndexGroupsByProject(t *testing.T) {
	base := t.TempDir()
	data, scratch := filepath.Join(base, "data"), filepath.Join(base, "scratch")
	defer func(orig func() []string) { tempRoots = orig }(tempRoots)
	tempRoots = func() []string { return []string{scratch} }

	repo := filepath.Join(base, "repo")
	wt := filepath.Join(base, "repo-wt")
	plain := filepath.Join(base, "plain")
	gone := filepath.Join(base, "gone")
	for _, d := range []string{filepath.Join(repo, ".git", "worktrees", "wt"), filepath.Join(repo, "web"), wt, plain, scratch} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644)

	clock := time.Now().Add(-time.Hour)
	add := func(cwd, prompt string) *Session {
		t.Helper()
		s, err := Create(Dir(data, cwd), Meta{Cwd: cwd})
		if err != nil {
			t.Fatal(err)
		}
		s.AppendMessage(llm.UserText(prompt), nil)
		s.Close()
		clock = clock.Add(time.Minute)
		os.Chtimes(s.Path, clock, clock)
		return s
	}
	add(repo, "root work")
	add(filepath.Join(repo, "web"), "web work")
	busy := add(wt, "worktree work")
	add(plain, "plain work")
	add(gone, "gone work")
	add(scratch, "scratch work")
	// An older folder name for the same directory: still the same project.
	old := filepath.Join(data, "sessions", "legacy-plain")
	legacy, _ := Create(old, Meta{Cwd: plain})
	legacy.AppendMessage(llm.UserText("legacy work"), nil)
	legacy.Close()

	w := status.NewWriter(busy.Path, "tui")
	w.Set(status.Report{State: status.Blocked, Kind: status.Question})

	projects, err := Index(data, IndexOptions{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Project{}
	for _, p := range projects {
		byName[p.Name] = p
	}
	if len(projects) != 3 || byName["repo"].Root != repo || byName["plain"].Root != plain || !byName["gone"].Missing {
		t.Fatalf("projects: %+v", projects)
	}
	r := byName["repo"]
	if len(r.Sessions) != 3 {
		t.Fatalf("repo sessions: %+v", r.Sessions)
	}
	if s := r.Sessions[0]; s.Title != "worktree work" || s.Subdir != "../repo-wt" || s.Status.State != status.Blocked || !s.Running {
		t.Fatalf("worktree session, newest: %+v", s)
	}
	if s := r.Sessions[1]; s.Subdir != "web" || s.Status.State != status.Idle || s.Running {
		t.Fatalf("subfolder session: %+v", s)
	}
	if n := len(byName["plain"].Sessions); n != 2 {
		t.Fatalf("plain has %d sessions, want the legacy folder's too", n)
	}

	all, _ := Index(data, IndexOptions{Scratch: true})
	if len(all) != 4 {
		t.Fatalf("with scratch: %d projects", len(all))
	}
}

func TestIndexLarikWorktrees(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	defer func(orig func() []string) { tempRoots = orig }(tempRoots)
	tempRoots = func() []string { return []string{filepath.Join(base, "scratch")} }

	repo := filepath.Join(base, "repo")
	store := worktree.Dir(filepath.Join(data, "worktrees"), repo)
	live, merged := filepath.Join(store, "fix-parser"), filepath.Join(store, "session-ab12cd")
	for _, d := range []string{filepath.Join(repo, ".git", "worktrees", "fix-parser"), live} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(live, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "fix-parser")+"\n"), 0o644)
	for _, cwd := range []string{repo, live, merged} {
		s, err := Create(Dir(data, cwd), Meta{Cwd: cwd})
		if err != nil {
			t.Fatal(err)
		}
		s.AppendMessage(llm.UserText("work in "+filepath.Base(cwd)), nil)
		s.Close()
	}

	projects, err := Index(data, IndexOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// One project: the live worktree's session and the merged one's under
	// the repository, by the worktree's name.
	if len(projects) != 1 || projects[0].Root != repo || len(projects[0].Sessions) != 3 {
		t.Fatalf("projects: %+v", projects)
	}
	subdirs := map[string]bool{}
	for _, s := range projects[0].Sessions {
		subdirs[s.Subdir] = true
	}
	if !subdirs[""] || !subdirs["fix-parser"] || !subdirs["session-ab12cd"] {
		t.Fatalf("subdirs: %v", subdirs)
	}
	// With the data dir in the temp folder, the worktrees are still the
	// repository's, not scratch.
	tempRoots = func() []string { return []string{data} }
	if projects, _ := Index(data, IndexOptions{}); len(projects) != 1 || len(projects[0].Sessions) != 3 {
		t.Fatalf("with the data dir in the temp folder: %+v", projects)
	}
}
