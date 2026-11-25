package changespane

import (
	"context"
	"os"
	"path/filepath"
)

func Stage(ctx context.Context, root string, paths []string, on bool) error {
	if on {
		_, err := GitDo(ctx, root, append([]string{"add", "-A", "--"}, paths...)...)
		return err
	}
	return Unstage(ctx, root, paths...)
}

func StageAll(ctx context.Context, root string, on bool) error {
	if on {
		_, err := GitDo(ctx, root, "add", "-A")
		return err
	}
	return Unstage(ctx, root)
}

// Unstage also handles a repository before its first commit.
func Unstage(ctx context.Context, root string, paths ...string) error {
	if _, err := GitDo(ctx, root, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		args := []string{"rm", "-r", "-q", "--cached", "--"}
		if len(paths) == 0 {
			paths = []string{"."}
		}
		_, err := GitDo(ctx, root, append(args, paths...)...)
		return err
	}
	_, err := GitDo(ctx, root, append([]string{"reset", "-q", "--"}, paths...)...)
	return err
}

func Discard(ctx context.Context, root, path, from, code string) error {
	switch {
	case code == "??":
		return os.Remove(filepath.Join(root, path))
	case code[0] == 'A' || from != "":
		if _, err := GitDo(ctx, root, "rm", "-q", "-f", "--", path); err != nil {
			return err
		}
		if from == "" {
			return nil
		}
		path = from
	}
	_, err := GitDo(ctx, root, "restore", "--source=HEAD", "--staged", "--worktree", "--", path)
	return err
}
