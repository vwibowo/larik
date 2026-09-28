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
		return len(parts) > 1 && (strings.EqualFold(parts[1], "hooks") || strings.EqualFold(parts[1], "config"))
	}
	return false
}
