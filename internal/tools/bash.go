package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"larik/internal/llm"
	"larik/internal/pathpolicy"
	"larik/internal/procgroup"
)

const (
	defaultBashTimeout = 2 * time.Minute
	maxBashTimeout     = 10 * time.Minute
	// PipeWaitDelay is how long a finished (or killed) command's output
	// pipes may stay open, held by a process it left running, before
	// Larik stops reading them.
	PipeWaitDelay = 2 * time.Second
)

// Bash runs a shell command in the working directory.
type Bash struct{}

func (Bash) ReadOnly() bool { return false }
func (Bash) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "bash",
		Description: "Run a shell command with bash in the working directory. Output (stdout+stderr) is truncated to ~30KB. Default timeout 120s, max 600s. Avoid interactive commands. " +
			"In a git repository, files the command changes (tracked, or untracked and not ignored) are recorded so /undo can restore them. " +
			"For files git ignores, or outside a repository, list the ones the command may change in checkpoint_paths; other changes there can't be undone. " +
			"When the system prompt has a <sandbox> section, commands run confined as it describes. " +
			"If a command genuinely needs more (installing packages, network access, writing elsewhere), run it again with sandbox set to false; the user will be asked to approve it.",
		Schema: schema(`{"type":"object","properties":{
			"command":{"type":"string"},
			"timeout":{"type":"integer","description":"Timeout in seconds (max 600)"},
			"checkpoint_paths":{"type":"array","items":{"type":"string"},"description":"Project files to snapshot before running the command for /undo"},
			"sandbox":{"type":"boolean","description":"Set false to run outside the sandbox; requires user approval"},
			"raw_output":{"type":"boolean","description":"Bypass token-saver filtering for this call"}},
			"required":["command"]}`),
	}
}

// lockedWriter lets stdout and stderr share one output stream safely.
type lockedWriter struct {
	mu       sync.Mutex
	w        io.Writer
	fallback io.Writer
	err      error
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		if l.fallback != nil {
			_, _ = l.fallback.Write(p)
		}
		return len(p), nil
	}
	n, err := l.w.Write(p)
	if err != nil || n != len(p) {
		if err == nil {
			err = io.ErrShortWrite
		}
		l.err = err
		if l.fallback != nil {
			_, _ = l.fallback.Write(p[n:])
		}
		return len(p), nil
	}
	return n, nil
}

func (Bash) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	in, err := decode[struct {
		Command         string   `json:"command"`
		Timeout         int      `json:"timeout"`
		Sandbox         *bool    `json:"sandbox"`
		CheckpointPaths []string `json:"checkpoint_paths"`
		RawOutput       bool     `json:"raw_output"`
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
	started := time.Now()
	var cmd *exec.Cmd
	if sandboxed {
		cmd = env.Sandbox.Command(in.Command, env.Cwd)
	} else {
		cmd = exec.Command("bash", "-c", in.Command)
		cmd.Dir = env.Cwd
	}
	cmd.Env = quietEnv(cmd.Env)
	// Note the repository's state, to make what the command changes undoable.
	track := env.trackShell(ctx)
	// Own process group so cancellation kills children too.
	procgroup.Configure(cmd)
	var buffer capBuffer // bounded: a chatty command can't exhaust memory
	// A script gets more of the output than the model would.
	script := InScript(ctx)
	if script {
		buffer.limit = scriptBashOutput / 2
	}
	var rawFile *os.File
	var captureErr error
	callID, _ := ctx.Value(callIDKey{}).(string)
	if env.TokenSaver != nil && env.TokenSaver.Load() && env.RawOutputDir != "" && callID != "" {
		rawFile, captureErr = env.rawOutputFile(callID)
	}
	var outputWriter io.Writer = &buffer
	if rawFile != nil {
		outputWriter = rawFile
	}
	out := lockedWriter{w: outputWriter, fallback: &buffer}
	cmd.Stdout, cmd.Stderr = &out, &out
	// A process left running in the background ("server &", a daemon)
	// keeps the output pipe open; don't wait for it past the command.
	cmd.WaitDelay = PipeWaitDelay
	// The Linux sandbox leaves placeholder files in the project while a
	// command runs; tell it when the command is over.
	finished := func(leftRunning bool) {
		if f, ok := env.Sandbox.(interface{ Finished(*exec.Cmd, bool) }); ok && sandboxed {
			f.Finished(cmd, leftRunning)
		}
	}
	if err := cmd.Start(); err != nil {
		finished(false)
		if rawFile != nil {
			rawFile.Close()
			os.Remove(rawFile.Name())
		}
		return errorf("%v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var runErr error
	select {
	case runErr = <-done:
	case <-ctx.Done():
		_ = procgroup.Kill(cmd)
		<-done
		runErr = ctx.Err()
	}
	leftRunning := errors.Is(runErr, exec.ErrWaitDelay)
	if leftRunning {
		runErr = nil // the command itself succeeded
	}
	finished(leftRunning)
	track.finish(ctx)
	if out.err != nil && captureErr == nil {
		captureErr = out.err
	}

	if rawFile != nil {
		if err := rawFile.Close(); err != nil {
			captureErr = err
		}
	}
	raw := buffer.String()
	largeRaw := false
	if rawFile != nil {
		var err error
		raw, largeRaw, err = rawPreview(rawFile.Name(), callID)
		if err != nil && captureErr == nil {
			captureErr = err
		}
		if err != nil {
			raw = ""
		}
		raw += buffer.String()
	}
	// Escape codes and progress-bar redraws are noise to the model; the
	// raw_output file keeps the exact bytes.
	raw = cleanTerminal(raw)
	output := Truncate(raw, MaxOutputBytes)
	if largeRaw && captureErr == nil {
		output = raw
	}
	if captureErr != nil {
		output += "\n[token saver unavailable; command output was not filtered and raw capture may be incomplete]"
	}
	var exitErr *exec.ExitError
	failed := errors.As(runErr, &exitErr)
	if rawFile != nil && captureErr == nil && !largeRaw && !in.RawOutput && (runErr == nil || failed) && len(raw) > 0 {
		if filtered, ok := filterCommandOutput(in.Command, raw, failed); ok {
			marker := fmt.Sprintf("\n[token saver: %d -> %d bytes of command output (estimate); exact output: raw_output tool_call_id=%s]", len(raw), len(filtered), callID)
			if len(filtered)+len(marker) < len(output) {
				output = filtered + marker
			}
		}
	}
	// notes are what Larik adds to the output; a script gets them apart.
	var notes []string
	note := func(s string) {
		output += "\n" + s
		notes = append(notes, s)
	}
	if leftRunning {
		note("[a process the command started is still running in the background; its later output isn't captured]")
	}
	if sandboxed {
		if nb, ok := env.Sandbox.(interface{ NetworkBlocked(time.Time) []string }); ok {
			if hosts := nb.NetworkBlocked(started); len(hosts) > 0 {
				note("[sandbox: the network proxy refused " + strings.Join(hosts, ", ") + ": not in sandbox.allowed_domains. The user can add domains to that list in their personal config; or run the command again with \"sandbox\": false, which the user will be asked to approve.]")
			}
		}
	}
	// data is what a script gets: the output, more of it than the model
	// sees and unfiltered, and the exit code, which isn't an error to it.
	data := func(code int) any {
		if !script {
			return nil
		}
		out, complete := buffer.view(scriptBashOutput), buffer.complete()
		if rawFile != nil && captureErr == nil {
			var err error
			if out, complete, err = readEnds(rawFile.Name(), scriptBashOutput); err != nil {
				out, complete = raw, false
			}
		}
		d := map[string]any{"output": cleanTerminal(out), "exit_code": code, "truncated": !complete}
		if rawFile != nil && captureErr == nil {
			d["raw_output_id"] = callID
		}
		if len(notes) > 0 {
			d["notes"] = notes
		}
		return d
	}
	switch {
	case errors.Is(runErr, context.DeadlineExceeded):
		return Result{Content: fmt.Sprintf("%s\n[command timed out after %s]", output, timeout), IsError: true}
	case errors.Is(runErr, context.Canceled):
		return Result{Content: output + "\n[command interrupted by user]", IsError: true}
	case errors.As(runErr, &exitErr):
		content := fmt.Sprintf("%s\n[exit code %d]", output, exitErr.ExitCode())
		if sandboxed && sandboxBlocked(output) {
			hint := "[sandbox: this looks blocked by the sandbox (writes outside the project and temp dirs, and network access, are not allowed). " +
				"If the command really needs that, run it again with \"sandbox\": false; the user will be asked to approve it.]"
			content += "\n" + hint
			notes = append(notes, hint)
		}
		return Result{Content: content, IsError: true, Data: data(exitErr.ExitCode())}
	case runErr != nil:
		return errorf("%v", runErr)
	}
	if output == "" {
		output = "(no output)"
	}
	return Result{Content: output, Data: data(0)}
}

// readEnds reads the file at path when it is at most budget bytes, and
// otherwise its start and end around the count of bytes left out.
func readEnds(path string, budget int) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", false, err
	}
	if fi.Size() <= int64(budget) {
		data, err := io.ReadAll(f)
		return string(data), true, err
	}
	head := make([]byte, budget*2/3)
	if _, err := io.ReadFull(f, head); err != nil {
		return "", false, err
	}
	tail := make([]byte, budget-len(head))
	if _, err := f.ReadAt(tail, fi.Size()-int64(len(tail))); err != nil && err != io.EOF {
		return "", false, err
	}
	h, t := string(head), string(tail)
	for !utf8.ValidString(h) && len(h) > 0 {
		h = h[:len(h)-1]
	}
	for !utf8.ValidString(t) && len(t) > 0 {
		t = t[1:]
	}
	return fmt.Sprintf("%s\n\n... [%d bytes truncated] ...\n\n%s", h, fi.Size()-int64(len(h)+len(t)), t), false, nil
}

func rawPreview(path, callID string) (string, bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", false, err
	}
	if fi.Size() <= 4*1024*1024 {
		data, err := os.ReadFile(path)
		return string(data), false, err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	prefix := make([]byte, 19_000)
	n, err := io.ReadFull(f, prefix)
	if err != nil {
		return "", false, err
	}
	suffix := make([]byte, 9_000)
	if _, err := f.Seek(-int64(len(suffix)), io.SeekEnd); err != nil {
		return "", false, err
	}
	m, err := io.ReadFull(f, suffix)
	if err != nil {
		return "", false, err
	}
	if bytes.IndexByte(prefix[:n], 0) >= 0 || bytes.IndexByte(suffix[:m], 0) >= 0 {
		return fmt.Sprintf("[binary command output: %d bytes; use raw_output tool_call_id=%s for exact bytes]", fi.Size(), callID), true, nil
	}
	head, tail := string(prefix[:n]), string(suffix[:m])
	for !utf8.ValidString(head) && len(head) > 0 {
		head = head[:len(head)-1]
	}
	for !utf8.ValidString(tail) && len(tail) > 0 {
		tail = tail[1:]
	}
	return fmt.Sprintf("%s\n\n... [%d bytes truncated] ...\n\n%s", head, fi.Size()-int64(n+m), tail), true, nil
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
