package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"larik/internal/llm"
	"larik/internal/pathpolicy"
)

const (
	defaultBashTimeout = 2 * time.Minute
	maxBashTimeout     = 10 * time.Minute
)

// Bash runs a shell command in the working directory.
type Bash struct{}

func (Bash) ReadOnly() bool { return false }
func (Bash) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "bash",
		Description: "Run a shell command with bash in the working directory. Output (stdout+stderr) is truncated to ~30KB. Default timeout 120s, max 600s. Avoid interactive commands. " +
			"List project files the command may change in checkpoint_paths to let /undo restore their original contents; unlisted changes cannot be undone. " +
			"When a sandbox is active (see the environment section), commands run confined: they can write only to the project, temp directories and build caches, and have no network except localhost. " +
			"If a command genuinely needs more (installing packages, network access, writing elsewhere), run it again with sandbox set to false; the user will be asked to approve it.",
		Schema: schema(`{"type":"object","properties":{
			"command":{"type":"string"},
			"timeout":{"type":"integer","description":"Timeout in seconds (max 600)"},
			"checkpoint_paths":{"type":"array","items":{"type":"string"},"description":"Project files to snapshot before running the command for /undo"},
			"sandbox":{"type":"boolean","description":"Set false to run outside the sandbox; requires user approval"}},
			"required":["command"]}`),
	}
}

// lockedBuffer lets stdout and stderr share one buffer safely.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (Bash) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	in, err := decode[struct {
		Command         string   `json:"command"`
		Timeout         int      `json:"timeout"`
		Sandbox         *bool    `json:"sandbox"`
		CheckpointPaths []string `json:"checkpoint_paths"`
	}](input)
	if err != nil {
		return errorf("%v", err)
	}
	if len(in.CheckpointPaths) > 0 && env.BeforeWrite == nil {
		return errorf("checkpoints are unavailable in this session")
	}
	paths := make([]string, 0, len(in.CheckpointPaths))
	for _, path := range in.CheckpointPaths {
		rel, err := pathpolicy.WritePath(env.Cwd, path)
		if err != nil {
			return errorf("checkpoint path %q: %v", path, err)
		}
		paths = append(paths, filepath.Join(env.Cwd, rel))
	}
	for _, path := range paths {
		if err := env.beforeWrite(path); err != nil {
			return errorf("checkpoint %q: %v", path, err)
		}
	}
	timeout := defaultBashTimeout
	if in.Timeout > 0 {
		timeout = min(time.Duration(in.Timeout)*time.Second, maxBashTimeout)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sandboxed := env.Sandbox != nil && (in.Sandbox == nil || *in.Sandbox)
	var cmd *exec.Cmd
	if sandboxed {
		cmd = env.Sandbox.Command(in.Command, env.Cwd)
	} else {
		cmd = exec.Command("bash", "-c", in.Command)
		cmd.Dir = env.Cwd
	}
	// Own process group so cancellation kills children too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out lockedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return errorf("%v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var runErr error
	select {
	case runErr = <-done:
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		runErr = ctx.Err()
	}

	output := Truncate(out.b.String(), MaxOutputBytes)
	var exitErr *exec.ExitError
	switch {
	case errors.Is(runErr, context.DeadlineExceeded):
		return Result{Content: fmt.Sprintf("%s\n[command timed out after %s]", output, timeout), IsError: true}
	case errors.Is(runErr, context.Canceled):
		return Result{Content: output + "\n[command interrupted by user]", IsError: true}
	case errors.As(runErr, &exitErr):
		content := fmt.Sprintf("%s\n[exit code %d]", output, exitErr.ExitCode())
		if sandboxed && sandboxBlocked(output) {
			content += "\n[sandbox: this looks blocked by the sandbox (writes outside the project and temp dirs, and network access, are not allowed). " +
				"If the command really needs that, run it again with \"sandbox\": false; the user will be asked to approve it.]"
		}
		return Result{Content: content, IsError: true}
	case runErr != nil:
		return errorf("%v", runErr)
	}
	if output == "" {
		output = "(no output)"
	}
	return Result{Content: output}
}

// sandboxMarkers are error texts typical of a denied write or network call.
var sandboxMarkers = []string{
	"Operation not permitted", "Read-only file system", "Permission denied",
	"Could not resolve host", "Couldn't connect to server", "Network is unreachable", "network is unreachable",
	"Temporary failure in name resolution", "nodename nor servname", "no such host", "getaddrinfo",
	"ENOTFOUND", "EAI_AGAIN", "ECONNREFUSED", "dial tcp", "Failed to establish a new connection",
}

func sandboxBlocked(output string) bool {
	for _, m := range sandboxMarkers {
		if strings.Contains(output, m) {
			return true
		}
	}
	return false
}
