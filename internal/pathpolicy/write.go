// Package pathpolicy defines the project boundary for Larik's file tools.
package pathpolicy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WritePath returns a path relative to cwd after rejecting paths outside the
// project, protected project files, and existing symlink components. Callers
// must still perform the write through os.Root to keep the project boundary
// intact if the filesystem changes after this check.
func WritePath(cwd, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("write path is empty")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	rel, err := filepath.Rel(cwd, filepath.Clean(path))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("write path must be inside the project")
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if protected(parts) {
		return "", fmt.Errorf("write path %q is protected", rel)
	}
	current := cwd
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("write path %q contains a symlink", rel)
		}
	}
	return rel, nil
}

func protected(parts []string) bool {
	switch strings.ToLower(parts[0]) {
	case ".larik", ".claude", ".mcp.json":
		return true
	case ".git":
		// .git itself (a gitdir file could point git elsewhere), and the
		// parts of the git dir that decide which code git runs.
		if len(parts) == 1 {
			return true
		}
		for _, name := range GitProtected {
			if strings.EqualFold(parts[1], name) {
				return true
			}
		}
	}
	return false
}

// GitProtected are the entries of a git directory that decide which code
// git runs: its config and hooks, and the files that point git at another
// directory's config and hooks (commondir, per-worktree config, submodule
// and worktree git dirs). Objects, refs and the index are left writable.
var GitProtected = []string{"hooks", "config", "config.worktree", "commondir", "info", "modules", "worktrees"}
