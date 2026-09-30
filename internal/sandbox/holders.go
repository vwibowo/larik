package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// bubblewrap makes a path read-only by binding it onto itself, which needs
// the path to exist. A protected path that doesn't exist yet (.mcp.json,
// .git/commondir) would be left creatable inside the writable project. So
// while a command runs, holders puts a harmless placeholder at each such
// path (at its first missing component), which bwrapArgs then binds
// read-only like any other protected path, and removes it again when the
// last command using it ends, unless something real has replaced it.
//
// The placeholders are in the real project, where other programs see them
// for as long as a command runs, so each is valid for what reads it: see
// placeholderContent.
type holders struct {
	mu   sync.Mutex
	refs map[string]int    // placeholder -> commands using it
	ours map[string]string // placeholders we made -> content ("/" for a directory)
	runs map[*exec.Cmd][]string
}

// dirNames are protected entries that are directories when real, and get
// an empty directory: the host can still use it meanwhile (git worktree
// add creating an entry under .git/worktrees).
var dirNames = map[string]bool{".larik": true, ".claude": true, ".git": true, "hooks": true, "info": true, "modules": true, "worktrees": true}

const dirHolder = "/"

// placeholderContent is what a file placeholder holds. An empty file would
// break its readers: git dies on an empty commondir ("." says the common
// directory is the git directory itself, as without the file), and an
// empty .mcp.json isn't JSON. An empty git config is a valid one.
func placeholderContent(name string) string {
	switch name {
	case "commondir":
		return ".\n"
	case ".mcp.json":
		return "{}\n"
	}
	return ""
}

func newHolders() *holders {
	return &holders{refs: map[string]int{}, ours: map[string]string{}, runs: map[*exec.Cmd][]string{}}
}

// hold makes the placeholders a command needs for the protected paths that
// are missing and counts them as in use; pass the result to started. A path
// counts as missing while it is only another running command's placeholder.
func (h *holders) hold(protected, writable []string) (held []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	missing := func(p string) bool {
		if _, ours := h.ours[p]; ours {
			return true
		}
		_, err := os.Lstat(p)
		return err != nil
	}
	seen := map[string]bool{}
	for _, p := range protected {
		if !missing(p) {
			continue
		}
		at := p
		for parent := filepath.Dir(at); parent != at && missing(parent); parent = filepath.Dir(at) {
			at = parent
		}
		// Outside the writable paths nothing can be created anyway.
		if seen[at] || !inside(at, writable) {
			continue
		}
		seen[at] = true
		if _, made := h.ours[at]; !made {
			content := placeholderContent(filepath.Base(at))
			var err error
			if at != p || dirNames[filepath.Base(at)] {
				content = dirHolder
				err = os.Mkdir(at, 0o755)
			} else {
				err = writeNew(at, content)
			}
			if err != nil {
				continue // left creatable, as before; nothing to clean up
			}
			h.ours[at] = content
		}
		h.refs[at]++
		held = append(held, at)
	}
	return held
}

// started ties held placeholders to cmd until finished(cmd).
func (h *holders) started(cmd *exec.Cmd, held []string) {
	if len(held) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.runs[cmd] = held
}

// writeNew creates path with content, never replacing a file that appeared
// since it was found missing.
func writeNew(path, content string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// inside reports whether p is strictly under one of dirs.
func inside(p string, dirs []string) bool {
	for _, d := range dirs {
		if rel, err := filepath.Rel(d, p); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// finished releases cmd's placeholders and removes those no other command
// uses. If the command left a process running, its sandbox lives on with
// the placeholders mounted, and removing one would unmount it there; those
// stay until close.
func (h *holders) finished(cmd *exec.Cmd, leftRunning bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	paths := h.runs[cmd]
	delete(h.runs, cmd)
	if leftRunning {
		return
	}
	for _, p := range paths {
		if h.refs[p]--; h.refs[p] > 0 {
			continue
		}
		delete(h.refs, p)
		h.remove(p)
	}
}

// remove deletes a placeholder if it is still what we made: an empty
// directory, or a file with our content. Anything else is real data that
// replaced it.
func (h *holders) remove(p string) {
	content, ok := h.ours[p]
	if !ok {
		return
	}
	delete(h.ours, p)
	if content == dirHolder {
		_ = os.Remove(p) // fails unless it's an empty directory
		return
	}
	if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() {
		return
	}
	if data, err := os.ReadFile(p); err == nil && string(data) == content {
		_ = os.Remove(p)
	}
}

// close removes every placeholder still there.
func (h *holders) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for p := range h.ours {
		h.remove(p)
	}
}
