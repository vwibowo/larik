// Package worktree gives subagents isolated git worktrees: a separate
// checkout on its own branch, so parallel agents can't clobber each other
// or the user's working tree.
package worktree

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BranchPrefix names the branches Larik creates.
const BranchPrefix = "larik/"

// Worktree is a checkout created for one task.
type Worktree struct {
	Name   string // task-xxxxxx
	Path   string
	Branch string
	Base   string // commit it started from
	Repo   string // main repository root
}

// Outcome is what Finish did with a worktree.
type Outcome struct {
	Kept      bool
	Committed bool     // uncommitted changes were committed on the branch
	Commits   int      // commits on the branch since Base
	Files     []string // files changed since Base
	Warning   string   // e.g. leftovers that could not be committed
}

// repoLocks serializes git operations that touch shared repository state
// (branch refs, worktree metadata) when tasks start or end in parallel.
var repoLocks sync.Map // repo root -> *sync.Mutex

func lock(repo string) func() {
	m, _ := repoLocks.LoadOrStore(repo, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Dir is where worktrees of repo live under root (the data dir).
func Dir(root, repo string) string {
	slug := strings.NewReplacer("/", "-", "\\", "-", ":", "").Replace(strings.Trim(repo, "/"))
	return filepath.Join(root, slug)
}

// Create adds a worktree of repo at HEAD on a new branch.
func Create(ctx context.Context, root, repo string) (*Worktree, error) {
	base, err := git(ctx, repo, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return nil, errors.New("worktree isolation needs a repository with at least one commit")
	}
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	name := "task-" + hex.EncodeToString(b)
	w := &Worktree{Name: name, Path: filepath.Join(Dir(root, repo), name), Branch: BranchPrefix + name, Base: base, Repo: repo}
	if err := os.MkdirAll(filepath.Dir(w.Path), 0o700); err != nil {
		return nil, err
	}
	unlock := lock(repo)
	defer unlock()
	if _, err := git(ctx, repo, "worktree", "add", "--quiet", "-b", w.Branch, w.Path, base); err != nil {
		return nil, fmt.Errorf("creating worktree: %w", err)
	}
	return w, nil
}

// CommonDir returns the repository's shared .git directory, which commits
// made in the worktree write to.
func (w *Worktree) CommonDir(ctx context.Context) string {
	d, err := git(ctx, w.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return filepath.Join(w.Repo, ".git")
	}
	return d
}

// Finish commits any leftover changes on the worktree's branch, then
// removes the worktree and branch if nothing changed, or keeps both.
func (w *Worktree) Finish(message string) (Outcome, error) {
	// The task's own context may be cancelled; finishing must still run.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var out Outcome

	status, err := git(ctx, w.Path, "status", "--porcelain")
	if err != nil {
		return out, err
	}
	if status != "" {
		if _, err := git(ctx, w.Path, "add", "-A"); err != nil {
			return out, err
		}
		args := []string{"commit", "--quiet", "--no-verify", "-m", message}
		if _, err := git(ctx, w.Path, args...); err != nil {
			// No identity configured: commit under a Larik one instead.
			if _, err2 := git(ctx, w.Path, append([]string{"-c", "user.name=Larik", "-c", "user.email=larik@localhost"}, args...)...); err2 != nil {
				out.Warning = "could not commit the remaining changes: " + err.Error()
			} else {
				out.Committed = true
			}
		} else {
			out.Committed = true
		}
	}

	count, err := git(ctx, w.Path, "rev-list", "--count", w.Base+"..HEAD")
	if err != nil {
		return out, err
	}
	out.Commits, _ = strconv.Atoi(count)
	if files, err := git(ctx, w.Path, "diff", "--name-only", w.Base, "HEAD"); err == nil && files != "" {
		out.Files = strings.Split(files, "\n")
	}
	if out.Commits > 0 || out.Warning != "" {
		out.Kept = true
		return out, nil
	}
	return out, w.Remove(ctx)
}

// Remove deletes the worktree and its branch.
func (w *Worktree) Remove(ctx context.Context) error {
	unlock := lock(w.Repo)
	defer unlock()
	if _, err := git(ctx, w.Repo, "worktree", "remove", "--force", w.Path); err != nil {
		return err
	}
	_, err := git(ctx, w.Repo, "branch", "-D", w.Branch)
	os.Remove(filepath.Dir(w.Path)) // the per-repository dir, once empty
	return err
}

// Report describes an outcome for the agent that delegated the task.
func (w *Worktree) Report(o Outcome) string {
	if !o.Kept {
		return "[worktree] The subagent made no changes; its worktree was removed."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[worktree] The subagent worked in an isolated git worktree. Its changes are on branch %s (%d commit(s) on top of %s), NOT in your working tree.",
		w.Branch, o.Commits, short(w.Base))
	if len(o.Files) > 0 {
		files := o.Files
		more := ""
		if len(files) > 20 {
			files, more = files[:20], fmt.Sprintf(" and %d more", len(o.Files)-20)
		}
		fmt.Fprintf(&b, "\nFiles changed: %s%s.", strings.Join(files, ", "), more)
	}
	if o.Warning != "" {
		fmt.Fprintf(&b, "\nWarning: %s. The worktree at %s still has them.", o.Warning, w.Path)
	}
	fmt.Fprintf(&b, "\nReview with `git diff %s...%s`, then bring the changes in with `git merge %s` (or `git cherry-pick`). "+
		"The worktree is at %s; when done, clean up with `git worktree remove %s && git branch -D %s`.",
		short(w.Base), w.Branch, w.Branch, w.Path, w.Path, w.Branch)
	return b.String()
}

// Info describes an existing Larik worktree.
type Info struct {
	Path    string
	Branch  string
	Commits int // ahead of the repository's HEAD
	Dirty   bool
}

// List returns Larik-created worktrees of repo.
func List(ctx context.Context, repo string) ([]Info, error) {
	out, err := git(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var list []Info
	var cur Info
	flush := func() {
		if strings.HasPrefix(cur.Branch, BranchPrefix) {
			if n, err := git(ctx, repo, "rev-list", "--count", "HEAD.."+cur.Branch); err == nil {
				cur.Commits, _ = strconv.Atoi(n)
			}
			if st, err := git(ctx, cur.Path, "status", "--porcelain"); err == nil && st != "" {
				cur.Dirty = true
			}
			list = append(list, cur)
		}
		cur = Info{}
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch refs/heads/"):
			cur.Branch = strings.TrimPrefix(line, "branch refs/heads/")
		}
	}
	flush()
	return list, nil
}

// Open returns the Larik worktree of repo with the given name or branch.
func Open(ctx context.Context, repo, name string) (*Worktree, error) {
	list, err := List(ctx, repo)
	if err != nil {
		return nil, err
	}
	for _, in := range list {
		if in.Branch == name || in.Branch == BranchPrefix+name || filepath.Base(in.Path) == name {
			return &Worktree{Name: strings.TrimPrefix(in.Branch, BranchPrefix), Path: in.Path, Branch: in.Branch, Repo: repo}, nil
		}
	}
	return nil, fmt.Errorf("no Larik worktree %q", name)
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		if s == "" {
			s = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], s)
	}
	return s, nil
}
