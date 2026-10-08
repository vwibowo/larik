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
// (start, result, results) and stdout (call, calls, print, done).

// CodeChildEnv, set to 1, makes ServeCodeIfChild run the script runner.
const CodeChildEnv = "LARIK_RUN_CODE_CHILD"

// codeExitMemory is the child's exit status when it hits the memory limit.
const codeExitMemory = 3

type codeMsg struct {
	Type string `json:"type"` // start, call, calls, result, results, print, done

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
	// calls, results: a tools.parallel batch and its results, in order.
	Calls   []Call    `json:"calls,omitempty"`
	Results []codeMsg `json:"results,omitempty"`

	// result, print, done: the tool's output, a printed line, or the
	// script's value; for done, Error says why the script failed.
	Text      string `json:"text,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	Images    int    `json:"images,omitempty"`
	Error     string `json:"error,omitempty"`
	HasValue  bool   `json:"has_value,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	// Value is a result's Data, which the script gets in place of Text.
	Value json.RawMessage `json:"value,omitempty"`
}

// Confiner is implemented by sandboxes that can run a program with no
// writes, no network and no personal files, which is all the runner needs.
type Confiner interface {
	// Confine wraps a command line whose argv[0] is an absolute path.
	Confine(argv []string) []string
}

// codeCommand prepares the runner, confined by env's sandbox when it has
// one that can (with the sandbox off, it runs like any other helper).
func codeCommand(ctx context.Context, env *Env) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	argv := []string{exe}
	if env != nil {
		if c, ok := env.Sandbox.(Confiner); ok && c != nil {
			argv = c.Confine(argv)
		}
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// Nothing from Larik's environment (API keys included) is needed.
	cmd.Env = []string{CodeChildEnv + "=1"}
	for _, k := range []string{"SYSTEMROOT", "GOCOVERDIR"} { // Windows needs SYSTEMROOT
		if v := os.Getenv(k); v != "" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	return cmd, nil
}

func (c codeTool) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
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
	runCtx, cancel := context.WithTimeout(context.WithValue(ctx, scriptKey{}, true), timeout)
	defer cancel()

	r := &codeRun{caller: caller, ctx: runCtx, env: env, callID: CallID(ctx)}
	done, err := r.run(c, in.Code)
	if r.stopped || done.Truncated {
		r.newline()
		r.write(fmt.Sprintf("[output stopped at %d MB]", codeMaxOutput>>20))
	}
	msg := done.Error
	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
		msg = fmt.Sprintf("Error: the script timed out after %s", timeout)
	case ctx.Err() != nil:
		msg = "Error: interrupted by user"
	case err != nil:
		msg = "Error: " + err.Error()
	}
	switch {
	case msg != "":
		r.newline()
		r.write(msg)
	case done.HasValue:
		r.newline()
		r.write("=> " + done.Text)
	}
	saved := r.closeFile()

	out := strings.TrimSpace(Truncate(r.out.String(), MaxOutputBytes))
	if out == "" && msg == "" {
		out = "(no output: print the result with console.log)"
	}
	if saved {
		out += fmt.Sprintf("\n[full output, %d bytes: raw_output tool_call_id=%s]", r.out.total, r.callID)
	}
	out += fmt.Sprintf("\n[%s: %d tool call%s]", CodeToolName, r.calls, plural(r.calls))
	return Result{Content: out, IsError: msg != ""}
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
	caller Caller
	env    *Env
	ctx    context.Context
	calls  int

	// out keeps the start and end of the output for the model. Once the
	// output is longer than the model may see, all of it also goes to
	// file, which raw_output reads by callID.
	out        capBuffer
	last       byte // the output's last byte
	stopped    bool // the output passed codeMaxOutput
	callID     string
	file       *os.File
	fileFailed bool
}

// print adds what the script printed to the output, up to codeMaxOutput.
func (r *codeRun) print(s string) {
	if r.stopped {
		return
	}
	if room := codeMaxOutput - int(r.out.total); len(s) > room {
		s, r.stopped = safeCut(s, max(room, 0)), true
	}
	r.write(s)
}

// write adds s to the output. Unlike print it has no limit, so the lines
// Larik adds at the end (a stop marker, an error, the value) always fit.
func (r *codeRun) write(s string) {
	if s == "" {
		return
	}
	if r.file == nil && !r.fileFailed && r.out.total+int64(len(s)) > MaxOutputBytes {
		r.openFile()
	}
	r.out.Write([]byte(s))
	if r.file != nil {
		if _, err := r.file.WriteString(s); err != nil {
			r.dropFile()
		}
	}
	r.last = s[len(s)-1]
}

// newline ends the output's last line, if it has one.
func (r *codeRun) newline() {
	if r.out.total > 0 && r.last != '\n' {
		r.write("\n")
	}
}

// openFile starts the file raw_output reads, with the output so far,
// which out still holds in full: it is no longer than MaxOutputBytes.
func (r *codeRun) openFile() {
	r.fileFailed = true // one try
	if r.env == nil || r.env.RawOutputDir == "" || r.callID == "" {
		return
	}
	f, err := r.env.rawOutputFile(r.callID)
	if err != nil {
		return
	}
	r.file, r.fileFailed = f, false
	if _, err := f.Write(append(append([]byte(nil), r.out.head...), r.out.tail...)); err != nil {
		r.dropFile()
	}
}

func (r *codeRun) dropFile() {
	r.file.Close()
	os.Remove(r.file.Name())
	r.file, r.fileFailed = nil, true
}

// closeFile finishes the output file and reports whether it was kept.
func (r *codeRun) closeFile() bool {
	if r.file == nil {
		return false
	}
	if err := r.file.Close(); err != nil {
		os.Remove(r.file.Name())
		r.file = nil
		return false
	}
	r.file = nil
	return true
}

// resultMsg is the message that answers a call with res.
func resultMsg(res Result) codeMsg {
	m := codeMsg{Type: "result", Text: res.Content, IsError: res.IsError, Images: len(res.Images)}
	if res.Data != nil {
		if b, err := json.Marshal(res.Data); err == nil {
			m.Value = b
		}
	}
	return m
}

func callLimitResult() Result {
	return Result{Content: fmt.Sprintf("the script reached its limit of %d tool calls", codeMaxCalls), IsError: true}
}

// call runs one call the script asked for.
func (r *codeRun) call(name string, input json.RawMessage) Result {
	if r.calls >= codeMaxCalls {
		return callLimitResult()
	}
	r.calls++
	return r.caller.CallTool(r.ctx, name, input)
}

// callMany runs a tools.parallel batch, concurrently when the caller can.
func (r *codeRun) callMany(calls []Call) []codeMsg {
	out := make([]codeMsg, len(calls))
	var run []Call
	var at []int
	for i, c := range calls {
		if r.calls >= codeMaxCalls {
			out[i] = resultMsg(callLimitResult())
			continue
		}
		r.calls++
		run, at = append(run, c), append(at, i)
	}
	var results []Result
	if b, ok := r.caller.(BatchCaller); ok && len(run) > 0 {
		results = b.CallTools(r.ctx, run)
	} else {
		for _, c := range run {
			results = append(results, r.caller.CallTool(r.ctx, c.Name, c.Input))
		}
	}
	for k, i := range at {
		res := Result{Content: "no result", IsError: true}
		if k < len(results) {
			res = results[k]
		}
		out[i] = resultMsg(res)
	}
	return out
}

// run returns the child's done message, or an error when the child ended
// without one (killed for time, out of memory, or crashed).
func (r *codeRun) run(c codeTool, code string) (codeMsg, error) {
	cmd, err := codeCommand(r.ctx, r.env)
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
			r.print(m.Text)
		case "call":
			if err := enc.Encode(resultMsg(r.call(m.Name, m.Input))); err != nil {
				return codeMsg{}, err
			}
		case "calls":
			if err := enc.Encode(codeMsg{Type: "results", Results: r.callMany(m.Calls)}); err != nil {
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
		done.HasValue, done.Text = true, safeCut(s.show(v), codeMaxValue)
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

	// bound maps each tool's name to the one it has on the tools object;
	// byName finds a tool by either.
	bound  map[string]string
	byName map[string]llm.ToolSpec
	parse  goja.Callable // JSON.parse
}

func (s *script) find(name string) (llm.ToolSpec, bool) {
	sp, ok := s.byName[name]
	return sp, ok
}

func (s *script) install() {
	console := s.vm.NewObject()
	for _, name := range []string{"log", "info", "warn", "error", "debug"} {
		_ = console.Set(name, s.print)
	}
	_ = s.vm.Set("console", console)
	_ = s.vm.Set("print", s.print)

	parse, _ := goja.AssertFunction(s.vm.Get("JSON").ToObject(s.vm).Get("parse"))
	s.parse = parse

	s.bound = scriptNames(s.specs)
	s.byName = make(map[string]llm.ToolSpec, 2*len(s.specs))
	t := s.vm.NewObject()
	for _, sp := range s.specs {
		name := sp.Name
		fn := func(call goja.FunctionCall) goja.Value { return s.invoke(name, call.Argument(0)) }
		for _, n := range []string{name, s.bound[name]} {
			s.byName[n] = sp
			_ = t.Set(n, fn)
		}
	}
	_ = t.Set("call", func(name string, args goja.Value) goja.Value { return s.invoke(name, args) })
	_ = t.Set("parallel", s.parallel)
	_ = t.Set("describe", func(name string) goja.Value {
		sp, ok := s.find(name)
		if !ok {
			s.throw("no tool named %q; tools.list() returns the names", name)
		}
		var sch any
		_ = json.Unmarshal(sp.Schema, &sch)
		return s.vm.ToValue(map[string]any{"name": s.bound[sp.Name], "description": sp.Description, "schema": sch})
	})
	_ = t.Set("list", func() []string {
		names := make([]string, len(s.specs))
		for i, sp := range s.specs {
			names[i] = s.bound[sp.Name]
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

// prepare checks a call and encodes its arguments; it counts the call
// against the limit. A call that may not be sent gets an error message.
func (s *script) prepare(name string, args goja.Value) (Call, string) {
	sp, ok := s.find(name)
	if !ok {
		return Call{}, fmt.Sprintf("no tool named %q is available to scripts; tools.list() returns the names", name)
	}
	if s.calls >= s.maxCalls {
		return Call{}, fmt.Sprintf("the script reached its limit of %d tool calls", s.maxCalls)
	}
	input := json.RawMessage(`{}`)
	if args != nil && !goja.IsUndefined(args) && !goja.IsNull(args) {
		b, err := json.Marshal(args.Export())
		if err != nil {
			return Call{}, fmt.Sprintf("%s: arguments aren't JSON: %v", name, err)
		}
		if len(b) > codeMaxInput {
			return Call{}, fmt.Sprintf("%s: arguments are %d MB, over the %d MB limit", name, len(b)>>20, codeMaxInput>>20)
		}
		input = b
	}
	s.calls++
	return Call{Name: sp.Name, Input: input}, ""
}

// receive waits for the parent's answer of type typ.
func (s *script) receive(typ string) codeMsg {
	var res codeMsg
	if err := s.dec.Decode(&res); err != nil || res.Type != typ {
		os.Exit(1) // the parent is gone
	}
	return res
}

// value is what a call's result gives the script: its Data, or its text;
// or, for a failed call without Data, the error message.
func (s *script) value(name string, res codeMsg) (goja.Value, string) {
	if len(res.Value) > 0 {
		if v, err := s.parse(goja.Undefined(), s.vm.ToValue(string(res.Value))); err == nil {
			return v, ""
		}
	}
	content := res.Text
	if res.Images > 0 {
		content += fmt.Sprintf("\n[%d image(s) omitted: scripts get text only]", res.Images)
	}
	if res.IsError {
		return nil, name + ": " + content
	}
	return s.vm.ToValue(content), ""
}

// invoke asks the parent to run a tool and waits for its result.
func (s *script) invoke(name string, args goja.Value) goja.Value {
	call, msg := s.prepare(name, args)
	if msg != "" {
		s.throw("%s", msg)
	}
	if err := s.send(codeMsg{Type: "call", Name: call.Name, Input: call.Input}); err != nil {
		os.Exit(1) // the parent is gone
	}
	v, msg := s.value(s.bound[call.Name], s.receive("result"))
	if msg != "" {
		s.throw("%s", msg)
	}
	return v
}

// parallel runs tools.parallel([{name, args}, ...]): the calls go to the
// parent in one batch, and each gets {ok: true, value} or {ok: false,
// error}, in order. A bad entry fails alone, like a failed call.
func (s *script) parallel(list goja.Value) goja.Value {
	var items []goja.Value
	if list == nil || s.vm.ExportTo(list, &items) != nil {
		s.throw("tools.parallel takes an array of {name, args}")
	}
	var calls []Call
	var names []string
	errs := make([]string, len(items))
	at := make([]int, len(items)) // index into calls, for entries sent
	for i, item := range items {
		at[i] = -1
		var name string
		var args goja.Value
		if o, ok := item.(*goja.Object); ok {
			n := o.Get("name")
			if n == nil || goja.IsUndefined(n) {
				n = o.Get("tool")
			}
			if n != nil && !goja.IsUndefined(n) {
				name = n.String()
			}
			args = o.Get("args")
		}
		if name == "" {
			errs[i] = fmt.Sprintf("entry %d: want {name, args}", i)
			continue
		}
		call, msg := s.prepare(name, args)
		if msg != "" {
			errs[i] = msg
			continue
		}
		at[i] = len(calls)
		calls, names = append(calls, call), append(names, s.bound[call.Name])
	}
	var results []codeMsg
	if len(calls) > 0 {
		if err := s.send(codeMsg{Type: "calls", Calls: calls}); err != nil {
			os.Exit(1)
		}
		results = s.receive("results").Results
	}
	out := make([]any, len(items))
	for i := range items {
		o := s.vm.NewObject()
		msg := errs[i]
		if k := at[i]; k >= 0 {
			if k >= len(results) {
				os.Exit(1) // the parent broke the protocol
			}
			var v goja.Value
			if v, msg = s.value(names[k], results[k]); msg == "" {
				_ = o.Set("value", v)
			}
		}
		_ = o.Set("ok", msg == "")
		if msg != "" {
			_ = o.Set("error", msg)
		}
		out[i] = o
	}
	return s.vm.NewArray(out...)
}

// print sends a line to the parent, up to codeMaxOutput bytes in all.
func (s *script) print(call goja.FunctionCall) goja.Value {
	if s.truncated {
		return goja.Undefined()
	}
	parts := make([]string, len(call.Arguments))
	for i, a := range call.Arguments {
		parts[i] = s.show(a)
	}
	line := strings.Join(parts, " ") + "\n"
	if room := codeMaxOutput - s.sent; len(line) > room {
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
