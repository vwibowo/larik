package tools

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// A shell command can change any file without saying which, so the file
// tools' way of making /undo work (snapshot a file just before writing it)
// doesn't apply. In a git repository there is another way: git knows every
// file that differs from the last commit, before and after the command.
//
//   - A file that was clean before and isn't after was changed by the
//     command, and its original content is still in git (the commit the
//     command started from).
//   - A file that was already modified or untracked has no copy in git, so
//     those are read before the command runs (there are usually few).
//   - A file that is untracked after and wasn't there before was created.
//
// What this can't see: files git ignores (build output, dependencies),
// clean files that a command changes by moving HEAD (checkout, reset, pull:
// they stay clean), and anything outside a repository. checkpoint_paths
// covers those when the model lists them.

const (
	// trackFileMax is the largest file kept for undo; trackTotalMax bounds
	// the files read before one command; trackCountMax is how many
	// already-changed files are worth tracking at all.
	trackFileMax  = 2 << 20
	trackTotalMax = 64 << 20
	trackCountMax = 2000
	trackTimeout  = 15 * time.Second
)

type fileState struct {
	existed bool
	data    []byte
	mode    os.FileMode
	skip    bool // too large, or not a regular file: left alone
}

// shellTrack is the state of the repository before a command.
type shellTrack struct {
	env *Env
	// cwd is env.Cwd with symlinks resolved, as git reports paths: a
	// project opened through a symlink (/var on macOS) must still match.
	cwd   string
	repo  string               // git top-level directory
	head  string               // commit the command started from; "" before the first
	dirty map[string]fileState // repo-relative path -> state before
}

// trackShell records what is needed to undo a shell command's changes. It
// returns nil when there is nothing to record them with, or no repository.
func (e *Env) trackShell(ctx context.Context) *shellTrack {
	if e.RecordOriginal == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, trackTimeout)
	defer cancel()
	repo, err := gitText(ctx, e.Cwd, "rev-parse", "--show-toplevel")
	if err != nil || repo == "" {
		return nil
	}
	if resolved, err := filepath.EvalSymlinks(repo); err == nil {
		repo = resolved
	}
	paths, ok := dirtyPaths(ctx, repo)
	if !ok || len(paths) > trackCountMax {
		return nil
	}
	t := &shellTrack{env: e, repo: repo, cwd: e.Cwd, dirty: map[string]fileState{}}
	if resolved, err := filepath.EvalSymlinks(e.Cwd); err == nil {
		t.cwd = resolved
	}
	t.head, _ = gitText(ctx, repo, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	total := 0
	for p := range paths {
		st := readState(filepath.Join(repo, p), trackFileMax)
		if total += len(st.data); total > trackTotalMax {
			st = fileState{skip: true}
		}
		t.dirty[p] = st
	}
	return t
}

// finish records the original state of every file the command changed and
// returns how many that was.
func (t *shellTrack) finish(ctx context.Context) int {
	if t == nil {
		return 0
	}
	// The command's context may be cancelled; the record still matters.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), trackTimeout)
	defer cancel()
	after, ok := dirtyPaths(ctx, t.repo)
	if !ok {
		return 0
	}
	recorded := 0
	record := func(p string, st fileState) {
		// Undo works inside the working directory, under the name the
		// session knows it by.
		rel, err := filepath.Rel(t.cwd, filepath.Join(t.repo, p))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return
		}
		if t.env.RecordOriginal(filepath.Join(t.env.Cwd, rel), st.existed, st.data, st.mode) == nil {
			recorded++
		}
	}
	// Files that were already changed before: compare with the copy taken.
	for p, before := range t.dirty {
		if before.skip {
			continue
		}
		now := readState(filepath.Join(t.repo, p), trackFileMax)
		if now.skip || now.existed != before.existed || !bytes.Equal(now.data, before.data) {
			record(p, before)
		}
	}
	// Files that were clean before and aren't now.
	for p, code := range after {
		if _, was := t.dirty[p]; was {
			continue
		}
		if code == "??" {
			record(p, fileState{}) // created by the command
			continue
		}
		if t.head == "" {
			continue
		}
		// It matched the commit the command started from: that is its original.
		data, err := gitBytes(ctx, t.repo, trackFileMax, "cat-file", "blob", t.head+":"+p)
		if err != nil {
			if code[0] == 'A' || code[1] == 'A' {
				record(p, fileState{}) // added and staged by the command
			}
			continue
		}
		mode := os.FileMode(0o644)
		if entry, _ := gitText(ctx, t.repo, "ls-tree", t.head, "--", p); strings.HasPrefix(entry, "100755") {
			mode = 0o755
		}
		record(p, fileState{existed: true, data: data, mode: mode})
	}
	return recorded
}

// readState reads a file as it is now. Anything but a regular file of at
// most limit bytes is marked skip.
func readState(path string, limit int64) fileState {
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return fileState{}
	}
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > limit {
		return fileState{skip: true}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fileState{skip: true}
	}
	return fileState{existed: true, data: data, mode: fi.Mode().Perm()}
}

// dirtyPaths lists the files that differ from HEAD or are untracked (not
// ignored), with their two-letter status code.
func dirtyPaths(ctx context.Context, repo string) (map[string]string, bool) {
	out, err := gitBytes(ctx, repo, 64<<20, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames")
	if err != nil {
		return nil, false
	}
	paths := map[string]string{}
	for _, entry := range bytes.Split(out, []byte{0}) {
		if len(entry) > 3 {
			paths[string(entry[3:])] = string(entry[:2])
		}
	}
	return paths, true
}

func gitText(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitBytes(ctx, dir, 1<<20, args...)
	return strings.TrimSpace(string(out)), err
}

// gitBytes runs a read-only git command directly (no shell), with settings
// that keep it from running programs the repository configures or taking
// locks a concurrent git would trip over.
func gitBytes(ctx context.Context, dir string, limit int, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-pager", "-c", "core.fsmonitor=false", "-c", "core.quotepath=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	if len(out) > limit {
		return nil, os.ErrInvalid // too large to keep for undo
	}
	return out, nil
}
