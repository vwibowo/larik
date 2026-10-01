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
	"runtime/debug"
	"runtime/metrics"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"

	"larik/internal/llm"
)

// A run_code script runs in a child process: the larik binary (or a test
// binary) started with CodeChildEnv set. The interpreter can't bound its own
// memory, and one built-in call such as Array(1e9).fill(0) can allocate
// gigabytes before it returns, so the limit is enforced by a watchdog in
// the child that exits the process; whatever a script does, Larik itself
// keeps running. The child has no work of its own: every tool call is a
// message to the parent, which runs it with the usual checks.
//
// Messages are JSON values, one after another, on the child's stdin
// (start, result) and stdout (call, print, done).

// CodeChildEnv, set to 1, makes ServeCodeIfChild run the script runner.
const CodeChildEnv = "LARIK_RUN_CODE_CHILD"

// codeExitMemory is the child's exit status when it hits the memory limit.
const codeExitMemory = 3

type codeMsg struct {
	Type string `json:"type"` // start, call, result, print, done

	// start
	Code     string         `json:"code,omitempty"`
	Specs    []llm.ToolSpec `json:"specs,omitempty"`
	MaxCalls int            `json:"max_calls,omitempty"`
	Memory   uint64         `json:"memory,omitempty"`
	// Deadline is when the child stops by itself, in case its parent
	// can no longer stop it.
	Deadline time.Time `json:"deadline,omitzero"`

	// call
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// result, print, done: the tool's output, a printed line, or the
	// script's value; for done, Error says why the script failed.
	Text      string `json:"text,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	Images    int    `json:"images,omitempty"`
	Error     string `json:"error,omitempty"`
	HasValue  bool   `json:"has_value,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// codeCommand starts the runner; tests may replace it.
var codeCommand = func(ctx context.Context) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, exe)
	// Nothing from Larik's environment (API keys included) is needed.
	cmd.Env = []string{CodeChildEnv + "=1"}
	for _, k := range []string{"SYSTEMROOT", "GOCOVERDIR"} { // Windows needs SYSTEMROOT
		if v := os.Getenv(k); v != "" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	return cmd, nil
}

func (c codeTool) Run(ctx context.Context, _ *Env, input json.RawMessage) Result {
	in, err := decode[struct {
		Code    string `json:"code"`
		Timeout int    `json:"timeout_seconds"`
	}](input)
	if err != nil {
		return errorf("%v", err)
	}
	if strings.TrimSpace(in.Code) == "" {
		return errorf("code is empty")
	}
	caller, ok := CallerFrom(ctx)
	if !ok {
		return errorf("%s is unavailable here", CodeToolName)
	}
	timeout := codeTimeoutDefault
	if in.Timeout > 0 {
		timeout = min(time.Duration(in.Timeout)*time.Second, codeTimeoutMax)
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	r := &codeRun{caller: caller, ctx: runCtx}
	done, err := r.run(c, in.Code)
	out := r.out.String()
	if r.truncated || done.Truncated {
		out += "\n[output stopped at 1 MB]"
	}
	if err == nil && done.Error == "" && done.HasValue {
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += "=> " + done.Text
	}
	footer := fmt.Sprintf("[%s: %d tool call%s]", CodeToolName, r.calls, plural(r.calls))
	msg := done.Error
	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
		msg = fmt.Sprintf("Error: the script timed out after %s", timeout)
	case ctx.Err() != nil:
		msg = "Error: interrupted by user"
	case err != nil:
		msg = "Error: " + err.Error()
	}
	if msg != "" {
		return Result{Content: Truncate(strings.TrimSpace(out+"\n"+msg), MaxOutputBytes) + "\n" + footer, IsError: true}
	}
	if strings.TrimSpace(out) == "" {
		out = "(no output: print the result with console.log)"
	}
	return Result{Content: Truncate(out, MaxOutputBytes) + "\n" + footer}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// codeRun is the parent's side of one script: it starts the child, runs
// the tool calls it asks for, and collects what it prints.
type codeRun struct {
	caller    Caller
	ctx       context.Context
	out       strings.Builder
	truncated bool
	calls     int
}

// run returns the child's done message, or an error when the child ended
// without one (killed for time, out of memory, or crashed).
func (r *codeRun) run(c codeTool, code string) (codeMsg, error) {
	cmd, err := codeCommand(r.ctx)
	if err != nil {
		return codeMsg{}, fmt.Errorf("starting the script runner: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return codeMsg{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return codeMsg{}, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, n: 64 << 10}
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return codeMsg{}, fmt.Errorf("starting the script runner: %w", err)
	}
	enc, dec := json.NewEncoder(stdin), json.NewDecoder(stdout)
	deadline, _ := r.ctx.Deadline()
	done, protoErr := r.converse(enc, dec, codeMsg{Type: "start", Code: code, Specs: c.specs, MaxCalls: codeMaxCalls, Memory: codeMemoryLimit, Deadline: deadline})
	stdin.Close()
	waitErr := cmd.Wait()
	if protoErr == nil {
		return done, nil
	}
	if r.ctx.Err() != nil {
		return codeMsg{}, r.ctx.Err() // the caller explains timeouts and interrupts
	}
	var exit *exec.ExitError
	if errors.As(waitErr, &exit) && exit.ExitCode() == codeExitMemory || strings.Contains(stderr.String(), "out of memory") {
		return codeMsg{}, fmt.Errorf("the script used more than %d MB of memory and was stopped", codeMemoryLimit>>20)
	}
	if tail := strings.TrimSpace(stderr.String()); tail != "" {
		return codeMsg{}, fmt.Errorf("the script runner stopped: %s", clipLine(tail, 300))
	}
	if waitErr != nil {
		return codeMsg{}, fmt.Errorf("the script runner stopped: %v", waitErr)
	}
	return codeMsg{}, fmt.Errorf("the script runner stopped: %v", protoErr)
}

// converse sends start and serves the child's messages until done.
func (r *codeRun) converse(enc *json.Encoder, dec *json.Decoder, start codeMsg) (codeMsg, error) {
	if err := enc.Encode(start); err != nil {
		return codeMsg{}, err
	}
	for {
		var m codeMsg
		if err := dec.Decode(&m); err != nil {
			return codeMsg{}, err
		}
		switch m.Type {
		case "print":
			if room := codeMaxBuffer - r.out.Len(); len(m.Text) > room {
				m.Text, r.truncated = safeCut(m.Text, max(room, 0)), true
			}
			r.out.WriteString(m.Text)
		case "call":
			res := Result{Content: fmt.Sprintf("the script reached its limit of %d tool calls", codeMaxCalls), IsError: true}
			if r.calls < codeMaxCalls {
				r.calls++
				res = r.caller.CallTool(r.ctx, m.Name, m.Input)
			}
			if err := enc.Encode(codeMsg{Type: "result", Text: res.Content, IsError: res.IsError, Images: len(res.Images)}); err != nil {
				return codeMsg{}, err
			}
		case "done":
			return m, nil
		default:
			return codeMsg{}, fmt.Errorf("unexpected message %q", m.Type)
		}
	}
}

// limitedWriter keeps the first n bytes written to it and drops the rest.
type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}

// ServeCodeIfChild runs the script runner and exits when this process was
// started as one (CodeChildEnv is set); otherwise it returns at once. The
// larik binary calls it first thing, and so must the TestMain of any test
// package that runs scripts.
func ServeCodeIfChild() {
	if os.Getenv(CodeChildEnv) != "1" {
		return
	}
	os.Exit(serveCode(os.Stdin, os.Stdout))
}

// serveCode runs one script, talking to the parent over r and w.
func serveCode(r io.Reader, w io.Writer) int {
	dec := json.NewDecoder(r)
	var start codeMsg
	if err := dec.Decode(&start); err != nil || start.Type != "start" {
		fmt.Fprintln(os.Stderr, "run_code: expected a start message")
		return 2
	}
	if start.Memory > 0 {
		// The garbage collector works harder near the limit, so only live
		// data trips the watchdog.
		debug.SetMemoryLimit(int64(start.Memory))
	}
	go watch(start.Memory, start.Deadline)
	s := &script{
		vm:       goja.New(),
		specs:    start.Specs,
		maxCalls: start.MaxCalls,
		send:     (&codeSender{enc: json.NewEncoder(w)}).send,
		dec:      dec,
	}
	s.vm.SetMaxCallStackSize(codeMaxStack)
	s.install()
	v, err := s.run(start.Code)
	done := codeMsg{Type: "done", Truncated: s.truncated}
	switch {
	case err != nil:
		done.Error = scriptError(err)
	case v != nil && !goja.IsUndefined(v) && !goja.IsNull(v):
		done.HasValue, done.Text = true, safeCut(s.show(v), codeMaxBuffer)
	}
	if s.send(done) != nil {
		return 1
	}
	return 0
}

// watch exits the process once the heap passes limit (0 for none). It
// runs beside the interpreter, so it also catches a single built-in call
// that allocates without returning to the script. It also ends a script
// whose parent has gone, or that is past its deadline (plus a grace
// period for the parent to stop it first), so none is left running.
func watch(limit uint64, deadline time.Time) {
	sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	parent := os.Getppid()
	for i := 0; ; i++ {
		if limit > 0 {
			metrics.Read(sample)
			if sample[0].Value.Kind() == metrics.KindUint64 && sample[0].Value.Uint64() > limit {
				os.Exit(codeExitMemory)
			}
		}
		if i%100 == 0 && (os.Getppid() != parent || !deadline.IsZero() && time.Now().After(deadline.Add(5*time.Second))) {
			os.Exit(1)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// codeSender writes messages to the parent one at a time.
type codeSender struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func (c *codeSender) send(m codeMsg) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enc.Encode(m)
}

// scriptError explains why a script failed.
func scriptError(err error) string {
	var ex *goja.Exception
	if errors.As(err, &ex) {
		// Drop the frames of the Go bindings; the script's lines remain.
		var lines []string
		for _, l := range strings.Split(ex.String(), "\n") {
			if !strings.HasSuffix(l, "(native)") {
				lines = append(lines, l)
			}
		}
		return strings.Join(lines, "\n")
	}
	var so *goja.StackOverflowError
	if errors.As(err, &so) {
		return fmt.Sprintf("RangeError: maximum call depth (%d) exceeded: check for runaway recursion", codeMaxStack)
	}
	return err.Error()
}

// script is the child's side of one run_code execution.
type script struct {
	vm        *goja.Runtime
	specs     []llm.ToolSpec
	maxCalls  int
	send      func(codeMsg) error
	dec       *json.Decoder
	sent      int // bytes of output sent
	truncated bool
	calls     int
}

func (s *script) find(name string) (llm.ToolSpec, bool) {
	for _, sp := range s.specs {
		if sp.Name == name {
			return sp, true
		}
	}
	return llm.ToolSpec{}, false
}

func (s *script) install() {
	console := s.vm.NewObject()
	for _, name := range []string{"log", "info", "warn", "error", "debug"} {
		_ = console.Set(name, s.print)
	}
	_ = s.vm.Set("console", console)
	_ = s.vm.Set("print", s.print)

	t := s.vm.NewObject()
	for _, sp := range s.specs {
		name := sp.Name
		_ = t.Set(name, func(call goja.FunctionCall) goja.Value { return s.invoke(name, call.Argument(0)) })
	}
	_ = t.Set("call", func(name string, args goja.Value) goja.Value { return s.invoke(name, args) })
	_ = t.Set("describe", func(name string) goja.Value {
		sp, ok := s.find(name)
		if !ok {
			s.throw("no tool named %q; tools.list() returns the names", name)
		}
		var sch any
		_ = json.Unmarshal(sp.Schema, &sch)
		return s.vm.ToValue(map[string]any{"name": sp.Name, "description": sp.Description, "schema": sch})
	})
	_ = t.Set("list", func() []string {
		names := make([]string, len(s.specs))
		for i, sp := range s.specs {
			names[i] = sp.Name
		}
		return names
	})
	_ = s.vm.Set("tools", t)
}

// run runs code as a script. Top-level await, which only async functions
// allow, is supported by wrapping code in one when it doesn't compile.
func (s *script) run(code string) (goja.Value, error) {
	prog, err := goja.Compile("script.js", code, false)
	if err != nil && strings.Contains(code, "await") {
		if wrapped, werr := goja.Compile("script.js", "(async () => {\n"+code+"\n})()", false); werr == nil {
			prog, err = wrapped, nil
		}
	}
	if err != nil {
		return nil, err
	}
	v, err := s.vm.RunProgram(prog)
	if err != nil {
		return nil, err
	}
	if p, ok := v.Export().(*goja.Promise); ok {
		switch p.State() {
		case goja.PromiseStateFulfilled:
			return p.Result(), nil
		case goja.PromiseStateRejected:
			return nil, errors.New(s.show(p.Result()))
		default:
			return nil, errors.New("the script's promise never settled")
		}
	}
	return v, nil
}

// throw raises a JavaScript Error, which a script can catch.
func (s *script) throw(format string, args ...any) {
	e, err := s.vm.New(s.vm.Get("Error"), s.vm.ToValue(fmt.Sprintf(format, args...)))
	if err != nil {
		panic(s.vm.NewGoError(fmt.Errorf(format, args...)))
	}
	panic(e)
}

// invoke asks the parent to run a tool and waits for its result.
func (s *script) invoke(name string, args goja.Value) goja.Value {
	if _, ok := s.find(name); !ok {
		s.throw("no tool named %q is available to scripts; tools.list() returns the names", name)
	}
	if s.calls >= s.maxCalls {
		s.throw("the script reached its limit of %d tool calls", s.maxCalls)
	}
	s.calls++
	input := json.RawMessage(`{}`)
	if args != nil && !goja.IsUndefined(args) && !goja.IsNull(args) {
		b, err := json.Marshal(args.Export())
		if err != nil {
			s.throw("%s: arguments aren't JSON: %v", name, err)
		}
		if len(b) > codeMaxInput {
			s.throw("%s: arguments are %d MB, over the %d MB limit", name, len(b)>>20, codeMaxInput>>20)
		}
		input = b
	}
	if err := s.send(codeMsg{Type: "call", Name: name, Input: input}); err != nil {
		os.Exit(1) // the parent is gone
	}
	var res codeMsg
	if err := s.dec.Decode(&res); err != nil || res.Type != "result" {
		os.Exit(1)
	}
	content := res.Text
	if res.Images > 0 {
		content += fmt.Sprintf("\n[%d image(s) omitted: scripts get text only]", res.Images)
	}
	if res.IsError {
		s.throw("%s: %s", name, content)
	}
	return s.vm.ToValue(content)
}

// print sends a line to the parent, up to codeMaxBuffer bytes in all.
func (s *script) print(call goja.FunctionCall) goja.Value {
	if s.truncated {
		return goja.Undefined()
	}
	parts := make([]string, len(call.Arguments))
	for i, a := range call.Arguments {
		parts[i] = s.show(a)
	}
	line := strings.Join(parts, " ") + "\n"
	if room := codeMaxBuffer - s.sent; len(line) > room {
		line, s.truncated = safeCut(line, max(room, 0)), true
	}
	s.sent += len(line)
	if s.send(codeMsg{Type: "print", Text: line}) != nil {
		os.Exit(1)
	}
	return goja.Undefined()
}

// show renders a value: primitives, functions and errors as JavaScript
// would print them, other objects as JSON.
func (s *script) show(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) {
		return "undefined"
	}
	o, ok := v.(*goja.Object)
	if !ok {
		return v.String()
	}
	if _, fn := goja.AssertFunction(v); fn || o.ClassName() == "Error" {
		return v.String()
	}
	if b, err := json.MarshalIndent(o.Export(), "", "  "); err == nil {
		return string(b)
	}
	return v.String()
}
