package session

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"larik/internal/status"
	"larik/internal/worktree"
)

// Project is a place sessions ran: a git repository, with its subfolders
// and worktrees, or a directory outside one.
type Project struct {
	Root     string    // the repository root, or the directory itself
	Name     string    // what to call it: the root's base name
	Missing  bool      // the folder no longer exists
	Scratch  bool      // under the system temp folder
	Updated  time.Time // the newest session's last change
	Sessions []Indexed // newest first
}

// Indexed is a session in the index, with what it is doing now.
type Indexed struct {
	Info
	// Subdir is where the session ran relative to the project root, or ""
	// at the root. A worktree's sessions show the worktree's own path, and
	// those in one of larik's own, kept in the data dir, its name.
	Subdir  string
	Status  status.Report // as of now; idle when nothing has reported
	Running bool          // a process has the session open
	Tab     string        // the larik desktop tab it runs in, while it runs
	PID     int           // the process that has it open, while it runs
}

// IndexOptions picks what Index includes.
type IndexOptions struct {
	// Scratch includes projects under the system temp folder, which are
	// mostly throwaway directories from tests and scripts.
	Scratch bool
}

// Index lists the sessions of every project under dataDir, grouped by
// the directory each one recorded when it started rather than by the
// folder it is filed in: folders from older versions hold sessions for
// the same directories as their replacements. Projects come newest first.
func Index(dataDir string, opts IndexOptions) ([]Project, error) {
	dirs, err := os.ReadDir(filepath.Join(dataDir, "sessions"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tmp := tempRoots()
	store := filepath.Join(dataDir, "worktrees")
	byRoot := map[string]*Project{}
	roots := map[string]string{} // cwd → project root, found once per directory
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		infos, err := List(filepath.Join(dataDir, "sessions", d.Name()))
		if err != nil {
			continue
		}
		for _, in := range infos {
			if in.Cwd == "" {
				continue
			}
			cwd := filepath.Clean(in.Cwd)
			root, ok := roots[cwd]
			if !ok {
				root = projectRoot(cwd)
				if _, err := os.Stat(cwd); err != nil && storedWorktree(store, cwd) != "" {
					root = cwd // gone; see foldWorktrees
				}
				roots[cwd] = root
			}
			// A larik worktree is scratch as its repository is, wherever
			// the data dir is.
			scratch := underAny(root, tmp) && (root != cwd || storedWorktree(store, cwd) == "")
			if scratch && !opts.Scratch {
				continue
			}
			p := byRoot[root]
			if p == nil {
				_, statErr := os.Stat(root)
				p = &Project{Root: root, Name: filepath.Base(root), Missing: os.IsNotExist(statErr), Scratch: scratch}
				byRoot[root] = p
			}
			s := Indexed{Info: in, Status: status.Report{State: status.Idle}}
			if name := storedWorktree(store, cwd); name != "" {
				s.Subdir = name
			} else if rel, err := filepath.Rel(root, cwd); err == nil && rel != "." {
				s.Subdir = rel
			}
			if f, ok := status.Read(in.Path); ok {
				s.Status, s.Running = f.Current(), f.Running()
				if s.Running {
					s.Tab, s.PID = f.Tab, f.PID
				}
			}
			p.Sessions = append(p.Sessions, s)
			if in.Modified.After(p.Updated) {
				p.Updated = in.Modified
			}
		}
	}
	foldWorktrees(store, byRoot)
	out := make([]Project, 0, len(byRoot))
	for _, p := range byRoot {
		slices.SortFunc(p.Sessions, func(a, b Indexed) int { return b.Modified.Compare(a.Modified) })
		out = append(out, *p)
	}
	slices.SortFunc(out, func(a, b Project) int {
		if c := b.Updated.Compare(a.Updated); c != 0 {
			return c
		}
		return strings.Compare(a.Root, b.Root)
	})
	return out, nil
}

// storedWorktree is the name of the larik worktree in store that dir is
// in, with the folder in it, or "" for a directory outside store.
// Worktrees there are <store>/<repository>/<name> (see worktree.Dir).
func storedWorktree(store, dir string) string {
	rel, err := filepath.Rel(store, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	_, name, ok := strings.Cut(rel, string(filepath.Separator))
	if !ok {
		return ""
	}
	return name
}

// foldWorktrees moves the sessions of larik worktrees that have been
// merged or removed back under their repositories, where they ran: the
// folder is gone, so projectRoot can't say whose it was, but the folder
// the worktree was in names the repository.
func foldWorktrees(store string, byRoot map[string]*Project) {
	for root, p := range byRoot {
		if !p.Missing || storedWorktree(store, root) == "" {
			continue
		}
		rel, _ := filepath.Rel(store, root)
		repoDir, _, _ := strings.Cut(rel, string(filepath.Separator))
		for _, q := range byRoot {
			if q.Missing || worktree.Dir(store, q.Root) != filepath.Join(store, repoDir) {
				continue
			}
			q.Sessions = append(q.Sessions, p.Sessions...)
			if p.Updated.After(q.Updated) {
				q.Updated = p.Updated
			}
			delete(byRoot, root)
			break
		}
	}
}

// projectRoot is the repository cwd belongs to, with a linked worktree
// counted as part of the repository it was made from, or cwd itself
// outside a repository. It only looks at the disk, so a project whose
// folder is gone is its own root.
func projectRoot(cwd string) string {
	for dir := cwd; ; {
		git := filepath.Join(dir, ".git")
		if fi, err := os.Lstat(git); err == nil {
			if fi.IsDir() {
				return dir
			}
			if main := worktreeOf(git); main != "" {
				return main
			}
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return cwd
		}
		dir = parent
	}
}

// worktreeOf reads a linked worktree's .git file, "gitdir: <repo>/.git/
// worktrees/<name>", and returns <repo>, or "" for anything else (a
// submodule's .git file points into the parent's .git/modules).
func worktreeOf(gitFile string) string {
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return ""
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return ""
	}
	gitdir = filepath.Clean(strings.TrimSpace(gitdir))
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(filepath.Dir(gitFile), gitdir)
	}
	sep := string(filepath.Separator)
	if i := strings.LastIndex(gitdir, sep+".git"+sep+"worktrees"+sep); i > 0 {
		return gitdir[:i]
	}
	return ""
}

// tempRoots are the folders scratch directories live under, as given and
// with symlinks resolved (/tmp is /private/tmp on macOS). A variable so
// tests, which run in the temp folder, can name another.
var tempRoots = func() []string {
	var out []string
	for _, d := range []string{os.TempDir(), "/tmp", "/var/tmp"} {
		out = append(out, filepath.Clean(d))
		if r, err := filepath.EvalSymlinks(d); err == nil {
			out = append(out, r)
		}
	}
	return out
}

func underAny(path string, roots []string) bool {
	for _, r := range roots {
		if rel, err := filepath.Rel(r, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
