package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"larik/internal/procgroup"
)

// maxBackgroundTimeout bounds a background command that sets a timeout;
// without one it runs until it exits, is stopped, or the session ends.
const maxBackgroundTimeout = 24 * time.Hour

// backgroundTail is how much of a finished background command's output
// goes into its notification; the rest stays in its log.
const backgroundTail = 4000

// BackgroundStarter runs fn as a background task of the agent that owns
// the tool call and returns the task's id. fn's context ends when the task
// is stopped or the session ends; its result is reported to the model
// when it returns.
type BackgroundStarter func(label string, fn func(ctx context.Context) (result string, failed bool)) (string, error)

type backgroundKey struct{}

// WithBackground lets tools run in ctx start background tasks. Only the
// main agent sets it: a subagent's run ends with nobody left to tell.
func WithBackground(ctx context.Context, start BackgroundStarter) context.Context {
	return context.WithValue(ctx, backgroundKey{}, start)
}

func backgroundOf(ctx context.Context) BackgroundStarter {
	start, _ := ctx.Value(backgroundKey{}).(BackgroundStarter)
	return start
}

// runBackground starts command as a background task. Its output goes to
// a log the model can read while it runs, and the end of it, with the exit
// code, arrives as a task notification when it exits.
func (env *Env) runBackground(ctx context.Context, command string, timeout time.Duration, sandboxed bool) Result {
	start := backgroundOf(ctx)
	if start == nil {
		return errorf("run_in_background isn't available here (subagents and this front end can't receive the result); run the command in the foreground")
	}
	callID := CallID(ctx)
	var log *os.File
	var err error
	if env.RawOutputDir != "" && callID != "" {
		log, err = env.rawOutputFile(callID)
	} else {
		log, err = os.CreateTemp("", "larik-bg-*.log")
	}
	if err != nil {
		return errorf("creating the output log: %v", err)
	}
	var cmd *exec.Cmd
	if sandboxed {
		cmd = env.Sandbox.Command(command, env.Cwd)
	} else {
		cmd = exec.Command("bash", "-c", command)
		cmd.Dir = env.Cwd
	}
	cmd.Env = env.commandEnv(cmd.Env)
	procgroup.Configure(cmd)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.WaitDelay = PipeWaitDelay
	finished := func(leftRunning bool) {
		if f, ok := env.Sandbox.(interface{ Finished(*exec.Cmd, bool) }); ok && sandboxed {
			f.Finished(cmd, leftRunning)
		}
	}
	if err := cmd.Start(); err != nil {
		finished(false)
		log.Close()
		os.Remove(log.Name())
		return errorf("%v", err)
	}
	// Where the model reads the output while the command runs.
	where := "read " + log.Name()
	if env.RawOutputDir != "" && callID != "" {
		where = "raw_output tool_call_id=" + callID
	}
	id, err := start(textLabel(command), func(tctx context.Context) (string, bool) {
		if timeout > 0 {
			var cancel context.CancelFunc
			tctx, cancel = context.WithTimeout(tctx, timeout)
			defer cancel()
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		var runErr error
		select {
		case runErr = <-done:
		case <-tctx.Done():
			_ = procgroup.Kill(cmd)
			<-done
			runErr = tctx.Err()
		}
		leftRunning := errors.Is(runErr, exec.ErrWaitDelay)
		if leftRunning {
			runErr = nil
		}
		finished(leftRunning)
		log.Close()
		tail, _, _ := readEnds(log.Name(), backgroundTail)
		out := cleanTerminal(tail)
		if out == "" {
			out = "(no output)"
		}
		var exitErr *exec.ExitError
		switch {
		case errors.Is(runErr, context.DeadlineExceeded):
			return fmt.Sprintf("%s\n[timed out after %s; full output: %s]", out, timeout, where), true
		case errors.Is(runErr, context.Canceled):
			return fmt.Sprintf("%s\n[stopped; full output: %s]", out, where), true
		case errors.As(runErr, &exitErr):
			return fmt.Sprintf("%s\n[exit code %d; full output: %s]", out, exitErr.ExitCode(), where), true
		case runErr != nil:
			return fmt.Sprintf("%s\n[%v]", out, runErr), true
		}
		return fmt.Sprintf("%s\n[exit code 0; full output: %s]", out, where), false
	})
	if err != nil {
		_ = procgroup.Kill(cmd)
		_ = cmd.Wait()
		finished(false)
		log.Close()
		return errorf("%v", err)
	}
	return Result{Content: fmt.Sprintf("Started %s in the background. Its output so far: %s. You'll get a notification when it exits; "+
		"task_wait blocks until then and task_stop kills it. In a non-interactive run it's stopped when you finish, so task_wait first if you need its result. "+
		"Its file changes aren't recorded for /undo.", id, where)}
}

// textLabel names a background command in task lists: its first line,
// shortened.
func textLabel(command string) string {
	const max = 60
	for i, r := range command {
		if r == '\n' {
			command = command[:i] + " …"
			break
		}
	}
	if len([]rune(command)) > max {
		command = string([]rune(command)[:max-1]) + "…"
	}
	return "bash: " + command
}
