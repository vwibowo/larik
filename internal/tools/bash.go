package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"larik/internal/llm"
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
		Name:        "bash",
		Description: "Run a shell command with bash in the working directory. Output (stdout+stderr) is truncated to ~30KB. Default timeout 120s, max 600s. Avoid interactive commands.",
		Schema: schema(`{"type":"object","properties":{
			"command":{"type":"string"},
			"timeout":{"type":"integer","description":"Timeout in seconds (max 600)"}},
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
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}](input)
	if err != nil {
		return errorf("%v", err)
	}
	timeout := defaultBashTimeout
	if in.Timeout > 0 {
		timeout = min(time.Duration(in.Timeout)*time.Second, maxBashTimeout)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.Command("bash", "-c", in.Command)
	cmd.Dir = env.Cwd
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
		return Result{Content: fmt.Sprintf("%s\n[exit code %d]", output, exitErr.ExitCode()), IsError: true}
	case runErr != nil:
		return errorf("%v", runErr)
	}
	if output == "" {
		output = "(no output)"
	}
	return Result{Content: output}
}
