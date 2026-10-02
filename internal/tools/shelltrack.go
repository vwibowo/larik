package tools

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	// size and modTime say whether the file may have changed since, so
	// an untouched one needn't be read again to compare.
	size    int64
	modTime time.Time
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
	repo := e.repoRoot(ctx)
	if repo == "" {
		return nil
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
	// Files that were already changed before: compare with the copy taken,
	// reading only those whose size or modification time moved.
	for p, before := range t.dirty {
		if before.skip || before.unchanged(filepath.Join(t.repo, p)) {
			continue
		}
		now := readState(filepath.Join(t.repo, p), trackFileMax)
		if now.skip || now.existed != before.existed || !bytes.Equal(now.data, before.data) {
			record(p, before)
		}
	}
	// Files that were clean before and aren't now.
	var fromHead []string
	for p, code := range after {
		if _, was := t.dirty[p]; was {
			continue
		}
		if code == "??" {
			record(p, fileState{}) // created by the command
			continue
		}
		if t.head != "" {
			fromHead = append(fromHead, p)
		}
	}
	// They matched the commit the command started from: that is their
	// original. One git process reads them all.
	originals := headFiles(ctx, t.repo, t.head, fromHead)
	for _, p := range fromHead {
		if st, ok := originals[p]; ok {
			record(p, st)
		} else if code := after[p]; code[0] == 'A' || code[1] == 'A' {
			record(p, fileState{}) // added and staged by the command
		}
	}
	return recorded
}

// unchanged reports whether the file at path still has the size and
// modification time it had when st was read.
func (st fileState) unchanged(path string) bool {
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return !st.existed
	}
	return err == nil && st.existed && fi.Mode().IsRegular() && fi.Size() == st.size && fi.ModTime().Equal(st.modTime) && fi.Mode().Perm() == st.mode
}

// headFiles reads paths as they are in commit head, with their modes, in
// one git cat-file --batch process. Paths missing from head, larger than
// trackFileMax, or not regular files are left out.
func headFiles(ctx context.Context, repo, head string, paths []string) map[string]fileState {
	out := map[string]fileState{}
	if len(paths) == 0 {
		return out
	}
	var in bytes.Buffer
	var asked []string
	for _, p := range paths {
		if strings.ContainsAny(p, "\n\r") {
			continue // can't be named on a --batch line
		}
		asked = append(asked, p)
		in.WriteString(head + ":" + p + "\n")
	}
	cmd := gitCommand(ctx, repo, "cat-file", "--batch")
	cmd.Stdin = &in
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return out
	}
	if err := cmd.Start(); err != nil {
		return out
	}
	r := bufio.NewReader(stdout)
	defer func() {
		io.Copy(io.Discard, r) // an early return must not leave git blocked writing
		cmd.Wait()
	}()
	for _, p := range asked {
		header, err := r.ReadString('\n')
		if err != nil {
			return out
		}
		// "<oid> <type> <size>", or "<name> missing".
		fields := strings.Fields(header)
		if len(fields) != 3 {
			continue
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return out
		}
		data := make([]byte, size+1) // content and its trailing newline
		if _, err := io.ReadFull(r, data); err != nil {
			return out
		}
		if fields[1] == "blob" && size <= trackFileMax {
			out[p] = fileState{existed: true, data: data[:size], mode: 0o644}
		}
	}
	// Executable bits come from the tree; links and submodules aren't
	// files to restore.
	for start := 0; start < len(asked); start += 500 {
		chunk := asked[start:min(start+500, len(asked))]
		listing, err := gitBytes(ctx, repo, 64<<20, append([]string{"ls-tree", "-z", head, "--"}, chunk...)...)
		if err != nil {
			continue
		}
		for _, entry := range bytes.Split(listing, []byte{0}) {
			// "<mode> <type> <oid>\t<path>"
			meta, path, ok := bytes.Cut(entry, []byte{'\t'})
			if !ok {
				continue
			}
			st, ok := out[string(path)]
			if !ok {
				continue
			}
			switch {
			case bytes.HasPrefix(meta, []byte("100755")):
				st.mode = 0o755
				out[string(path)] = st
			case !bytes.HasPrefix(meta, []byte("100644")):
				delete(out, string(path))
			}
		}
	}
	return out
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
	return fileState{existed: true, data: data, mode: fi.Mode().Perm(), size: fi.Size(), modTime: fi.ModTime()}
}

// repoRoot is the git top-level directory containing Cwd, symlinks
// resolved, or "" outside a repository.
func (e *Env) repoRoot(ctx context.Context) string {
	e.mu.Lock()
	repo := e.repo
	e.mu.Unlock()
	if repo != "" {
		if _, err := os.Stat(filepath.Join(repo, ".git")); err == nil {
			return repo
		}
	}
	repo, err := gitText(ctx, e.Cwd, "rev-parse", "--show-toplevel")
	if err != nil || repo == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(repo); err == nil {
		repo = resolved
	}
	e.mu.Lock()
	e.repo = repo
	e.mu.Unlock()
	return repo
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

// gitBytes runs a gitCommand and returns its output, at most limit bytes.
func gitBytes(ctx context.Context, dir string, limit int, args ...string) ([]byte, error) {
	out, err := gitCommand(ctx, dir, args...).Output()
	if err != nil {
		return nil, err
	}
	if len(out) > limit {
		return nil, os.ErrInvalid // too large to keep for undo
	}
	return out, nil
}

// gitCommand is a read-only git command run directly (no shell), with
// settings that keep it from running programs the repository configures
// or taking locks a concurrent git would trip over.
func gitCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-pager", "-c", "core.fsmonitor=false", "-c", "core.quotepath=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	return cmd
}
