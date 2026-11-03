package desktop

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"larik/internal/session"
	"larik/internal/status"
	"larik/internal/worktree"
)

// Worktrees: a session can start in a worktree of its own, a checkout
// on a new branch, so it works apart from yours and from the sessions
// beside it, as cmux and herdr run agents side by side. They're the
// worktrees isolated subagents use: under the data dir, on larik/
// branches, so larik's /worktrees lists them too. When the work is done,
// the session's menu merges the branch into the branch your checkout is
// on, or removes it; its tabs close, and its sessions stay, under the
// project.

// worktreeTimeout bounds the git a worktree's making, merging or
// removing runs: a checkout of a large repository takes a while.
const worktreeTimeout = 2 * time.Minute

// worktreeRoot is where the window's worktrees, and subagents', live.
func (a *App) worktreeRoot() string { return filepath.Join(a.opts.DataDir, "worktrees") }

// inWorktree reports whether dir is in one of larik's worktrees.
func (a *App) inWorktree(dir string) bool {
	return dir != "" && worktreeName(a.worktreeRoot(), dir) != ""
}

// worktreeName is the name of the worktree in root that dir is in, or "".
func worktreeName(root, dir string) string {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// makeWorktree makes a worktree, on the branch larik/<name> (or one
// named for it), of the repository dir is in: off the main thread, as
// git checks the files out.
func (a *App) makeWorktree(dir, name string) (*worktree.Worktree, error) {
	ctx, cancel := context.WithTimeout(context.Background(), worktreeTimeout)
	defer cancel()
	repo, err := worktree.Repo(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("a worktree needs a git repository, and %s isn't in one", tildePath(dir))
	}
	if name == "" {
		name = "session-" + newTabID()[:6]
	}
	return worktree.CreateNamed(ctx, a.worktreeRoot(), repo, name)
}

// newWorktreeSession starts a session in a new worktree of the
// repository dir is in.
func (a *App) newWorktreeSession(dir string) {
	target := a.focus
	a.notice = "Making a worktree of " + tildePath(dir) + "…"
	go func() {
		w, err := a.makeWorktree(dir, "")
		a.update(func() {
			a.notice = ""
			if err != nil {
				a.notice = "Can't make a worktree: " + err.Error()
				return
			}
			if target != nil && a.paneOf(target.tab) == target {
				a.focusPane(target)
			}
			if t := a.startTab(w.Path, "", ""); t != nil {
				a.activate(t)
			}
		})
	}()
}

// worktreeTabs are the tabs working in the worktree at dir: larik's,
// by the session they have open or where they started, and terminals,
// by where they started.
func (a *App) worktreeTabs(dir string) []*tab {
	var out []*tab
	for _, t := range a.tabs {
		where := t.dir
		if s, _, ok := a.find(t.path); ok {
			where = s.Cwd
		}
		if where == dir || strings.HasPrefix(where, dir+string(filepath.Separator)) {
			out = append(out, t)
		}
	}
	return out
}

// worktreeBusy says what keeps the worktree at dir from being merged or
// removed now: a session in it working or waiting for you, or open in
// another terminal; or "".
func (a *App) worktreeBusy(dir string) string {
	for _, t := range a.worktreeTabs(dir) {
		if st := a.tabStatus(t).State; st == status.Working || st == status.Blocked {
			return fmt.Sprintf("“%s” is %s in the worktree: let it finish, or close its tab, first.", a.tabTitle(t), map[status.State]string{status.Working: "working", status.Blocked: "waiting for you"}[st])
		}
	}
	for _, p := range a.projects {
		for _, s := range p.Sessions {
			if s.Running && !a.ours(s.Tab) && (s.Cwd == dir || strings.HasPrefix(s.Cwd, dir+string(filepath.Separator))) {
				return fmt.Sprintf("“%s” is open in another terminal, in the worktree: quit larik there first.", cmp.Or(s.Title, "A session"))
			}
		}
	}
	return ""
}

// worktreeMenu adds what can be done with the worktree a session runs
// in to its menu: merging it into the branch the project's checkout is
// on, and removing it.
func (a *App) worktreeMenu(m *ui.Menu, s session.Indexed, root string) {
	if !a.inWorktree(s.Cwd) {
		return
	}
	if _, err := os.Stat(s.Cwd); err != nil {
		return // merged or removed already
	}
	m.Separator()
	into := "the Checkout"
	if b := a.git[root].Branch; b != "" {
		into = b
	}
	if m.Item("Merge Worktree into " + into + "…").Chosen() {
		a.askWorktree(s, root, true)
	}
	if m.Item("Remove Worktree…").Chosen() {
		a.askWorktree(s, root, false)
	}
}

// askWorktree asks whether to merge, or remove, the worktree the session
// runs in, saying what it has that the checkout doesn't, which git is
// asked off the main thread.
func (a *App) askWorktree(s session.Indexed, root string, merge bool) {
	dir := worktreeDir(a.worktreeRoot(), s.Cwd)
	a.notice = a.worktreeBusy(dir)
	if a.notice != "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), worktreeTimeout)
		defer cancel()
		info, w, err := openWorktree(ctx, root, dir)
		a.update(func() {
			if err != nil {
				a.notice = "Can't find the worktree: " + err.Error()
				return
			}
			into := cmp.Or(a.git[root].Branch, "the checkout")
			has, one := worktreeHas(info, into)
			come, are := " come into ", " are lost. "
			if one {
				come, are = " comes into ", " is lost. "
			}
			if merge {
				msg := has + come + into + " in " + tildePath(root) + ". "
				if info.Commits == 0 && !info.Dirty {
					msg = "It has nothing that isn't in " + into + " already. "
				}
				a.confirmAsk(fmt.Sprintf("Merge “%s” into %s?", w.Branch, into),
					msg+"Then the worktree and its branch are removed, and its tabs close; its sessions stay.",
					"Merge", func() { a.finishWorktree(w, s.Title, true) })
				return
			}
			msg := "It has nothing that isn't in " + into + ". "
			if info.Commits > 0 || info.Dirty {
				msg = has + are
			}
			a.confirmAsk(fmt.Sprintf("Remove “%s”?", w.Branch),
				msg+"The worktree and its branch are removed, and its tabs close; its sessions stay.",
				"Remove", func() { a.finishWorktree(w, "", false) })
		})
	}()
}

// worktreeDir is the worktree in root that dir is in, its top folder.
func worktreeDir(root, dir string) string {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return dir
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) < 2 {
		return dir
	}
	return filepath.Join(root, parts[0], parts[1])
}

// openWorktree finds the larik worktree of repo at dir.
func openWorktree(ctx context.Context, repo, dir string) (worktree.Info, *worktree.Worktree, error) {
	list, err := worktree.List(ctx, repo)
	if err != nil {
		return worktree.Info{}, nil, err
	}
	w, err := worktree.Open(ctx, repo, dir)
	if err != nil {
		return worktree.Info{}, nil, err
	}
	// As the window knows it: git may have it with symlinks resolved.
	w.Path = dir
	for _, in := range list {
		if in.Branch == w.Branch {
			return in, w, nil
		}
	}
	return worktree.Info{}, w, nil
}

// worktreeHas says what a worktree has that the checkout on into
// doesn't, "Its 2 commits that aren't in main, and its uncommitted
// changes", and whether that's one thing.
func worktreeHas(in worktree.Info, into string) (string, bool) {
	var parts []string
	switch in.Commits {
	case 0:
	case 1:
		parts = append(parts, "its commit that isn't in "+into)
	default:
		parts = append(parts, fmt.Sprintf("its %d commits that aren't in %s", in.Commits, into))
	}
	if in.Dirty {
		parts = append(parts, "its uncommitted changes")
	}
	s := strings.Join(parts, ", and ")
	if s == "" {
		return "", false
	}
	return strings.ToUpper(s[:1]) + s[1:], in.Commits == 1 && !in.Dirty
}

// finishWorktree merges the worktree, when merge says to, committing
// what's left in it as title says; then closes its tabs and removes it.
// A merge that fails leaves everything as it was, and says why.
func (a *App) finishWorktree(w *worktree.Worktree, title string, merge bool) {
	if why := a.worktreeBusy(w.Path); why != "" {
		a.notice = why
		return
	}
	a.notice = "Removing " + w.Branch + "…"
	if merge {
		a.notice = "Merging " + w.Branch + "…"
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), worktreeTimeout)
		defer cancel()
		var got worktree.Merged
		if merge {
			var err error
			if got, err = w.Merge(ctx, cmp.Or(title, "Work from "+w.Branch)); err != nil {
				a.update(func() { a.notice = mergeFailed(w, err) })
				return
			}
		}
		closed := make(chan struct{})
		var stopped []<-chan error
		a.update(func() {
			defer close(closed)
			for _, t := range a.worktreeTabs(w.Path) {
				if done := a.closeTab(t); done != nil {
					stopped = append(stopped, done)
				}
			}
		})
		select {
		case <-closed:
		case <-a.done:
			return
		}
		for _, done := range stopped {
			select {
			case err := <-done:
				if err != nil {
					a.update(func() { a.notice = "Couldn't stop a tab before removing the worktree: " + err.Error() })
					return
				}
			case <-ctx.Done():
				a.update(func() { a.notice = "Couldn't stop a tab before removing the worktree: " + ctx.Err().Error() })
				return
			case <-a.done:
				return
			}
		}
		err := w.Remove(ctx)
		a.update(func() {
			a.refreshIndex()
			switch {
			case err != nil && merge:
				a.notice = fmt.Sprintf("Merged %s into %s, but couldn't remove the worktree: %v", w.Branch, got.Into, err)
			case err != nil:
				a.notice = "Couldn't remove " + w.Branch + ": " + err.Error()
			case merge && got.Commits == 0:
				a.notice = fmt.Sprintf("Nothing in %s to merge into %s: removed it.", w.Branch, got.Into)
			case merge:
				a.notice = fmt.Sprintf("Merged %s into %s: %s, %s.", w.Branch, got.Into, plural(got.Commits, "commit"), plural(len(got.Files), "file"))
			default:
				a.notice = "Removed " + w.Branch + "."
			}
		})
	}()
}

// mergeFailed says why a worktree didn't merge.
func mergeFailed(w *worktree.Worktree, err error) string {
	var conflict *worktree.ConflictError
	if errors.As(err, &conflict) {
		return fmt.Sprintf("%s conflicts with %s in %s, so nothing was merged. Merge it in a terminal to settle them: git merge %s",
			w.Branch, conflict.Into, strings.Join(conflict.Files, ", "), w.Branch)
	}
	return "Couldn't merge " + w.Branch + ": " + err.Error()
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}
