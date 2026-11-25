package changespane

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func GitDo(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	out, err := cmd.CombinedOutput()
	said := strings.TrimSpace(string(out))
	if err != nil {
		if said == "" {
			said = err.Error()
		}
		return said, fmt.Errorf("git %s: %s", args[0], said)
	}
	return said, nil
}

// ghDo runs gh in dir, failing with what it said.
func GhDo(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1")
	out, err := cmd.CombinedOutput()
	said := strings.TrimSpace(string(out))
	if err != nil {
		if said == "" {
			said = err.Error()
		}
		return said, fmt.Errorf("gh %s: %s", strings.Join(args[:min(2, len(args))], " "), said)
	}
	return said, nil
}

// Git runs a read-only Git command without optional repository locks.
func Git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "--no-optional-locks"}, args...)...)
	return cmd.Output()
}
