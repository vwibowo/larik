package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dop251/goja"

	"larik/internal/llm"
)

// Execution is how the model carries out actions: one tool call per step
// (the tool chain), or also by writing a script that calls tools
// (run_code). It is independent of the permission mode: every call a
// script makes is authorized like a direct one.
type Execution string

const (
	// ExecTools offers the tools only; this is the default.
	ExecTools Execution = "tools"
	// ExecHybrid offers the tools and run_code; the model picks per step.
	ExecHybrid Execution = "hybrid"
	// ExecCode offers run_code only; every tool is reached from scripts.
	ExecCode Execution = "code"
)

func ParseExecution(s string) (Execution, error) {
	switch e := Execution(s); e {
	case ExecTools, ExecHybrid, ExecCode:
		return e, nil
	case "":
		return ExecTools, nil
	}
	return "", fmt.Errorf("unknown execution %q (tools, hybrid, code)", s)
}

// CodeToolName runs a script that calls tools.
const CodeToolName = "run_code"

const (
	codeTimeoutDefault = 120 * time.Second
	codeTimeoutMax     = 600 * time.Second
	// codeMaxCalls bounds the tool calls one script may make.
	codeMaxCalls = 200
	// codeMaxBuffer bounds the output a script collects before it is
	// truncated to MaxOutputBytes for the model.
	codeMaxBuffer = 1 << 20
)

// unbound are tools a script can't call: the wrappers it replaces, itself,
// and tools that only make sense as a step of the conversation.
var unbound = map[string]bool{
	CodeToolName: true, ToolSearchName: true, CallToolName: true,
	"todo_write": true, "task": true, "task_stop": true, "task_wait": true,
	"exit_plan_mode": true, "skill": true, "memory": true,
}

// Caller runs one tool call on behalf of a script, with the same
// permission checks, hooks and events as a call the model makes directly.
type Caller interface {
	CallTool(ctx context.Context, name string, input json.RawMessage) Result
}

type callerKey struct{}

// WithCaller lets tools that make tool calls (run_code) reach the agent.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFrom returns the Caller set with WithCaller.
func CallerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}

// ForExecution returns the registry the model sees under e. Hybrid adds
// run_code; code leaves run_code as the only tool sent to the model, while
// every tool can still be looked up by name for the calls scripts make.
func (r *Registry) ForExecution(e Execution) *Registry {
	if e != ExecHybrid && e != ExecCode {
		return r
	}
	specs := r.bindable()
	if e == ExecHybrid {
		// The model already has the declared tools' schemas; index only
		// the deferred ones, which it doesn't.
		var listed []llm.ToolSpec
		for _, sp := range specs {
			if r.IsDeferred(sp.Name) {
				listed = append(listed, sp)
			}
		}
		return r.With(codeTool{specs: specs, listed: listed, hybrid: true})
	}
	nr := NewRegistry(codeTool{specs: specs, listed: specs})
	nr.deferred = r.deferred
	for name, t := range r.byName {
		if _, ok := nr.byName[name]; !ok {
			nr.byName[name] = t
		}
	}
	return nr
}

// bindable lists the specs of the tools scripts may call, sorted by name.
func (r *Registry) bindable() []llm.ToolSpec {
	var specs []llm.ToolSpec
	for _, t := range append(append([]Tool(nil), r.list...), r.deferred...) {
		if sp := t.Spec(); !unbound[sp.Name] {
			specs = append(specs, sp)
		}
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs
}

// codeTool is run_code. specs are the tools scripts may call; listed are
// those its description indexes: all of them when run_code is the only
// tool, only the undeclared (deferred) ones in hybrid.
type codeTool struct {
	specs, listed []llm.ToolSpec
	hybrid        bool
}

// ReadOnly is false so run_code runs alone; the agent doesn't authorize
// run_code itself but each call the script makes.
func (codeTool) ReadOnly() bool { return false }

func (c codeTool) Spec() llm.ToolSpec {
	var d strings.Builder
	if c.hybrid {
		d.WriteString("Run a JavaScript script that calls your tools, and get back only what it prints. " +
			"Use it when a task has many steps or a lot of data (loops over files, chained lookups, counting or filtering large output); " +
			"call a tool directly for a single action or an edit you need to look at first.\n\n")
	} else {
		d.WriteString("Run a JavaScript script that calls tools, and get back only what it prints. " +
			"This is how you use tools: each one below is a function in the script. Do several steps in one script when you can.\n\n")
	}
	fmt.Fprintf(&d, "tools.<name>(args) runs a tool with the arguments of its schema and returns its text output, or throws an Error with the tool's message; tools.call(name, args) is the same. "+
		"tools.describe(name) returns a tool's schema, tools.list() the names. console.log prints, and the last expression's value is printed too. "+
		"Calls are synchronous (no await). There is no file, network or process access except through tools, and every call is permission-checked. "+
		"Print summaries, not raw dumps. Limits: %d tool calls and %s per script (timeout_seconds, up to %d).",
		codeMaxCalls, codeTimeoutDefault, int(codeTimeoutMax.Seconds()))
	switch {
	case !c.hybrid:
		d.WriteString("\n\nTools (? marks optional arguments):\n" + c.index())
	case len(c.listed) > 0:
		d.WriteString("\n\nEvery tool you have works this way, with the same arguments, and so do these tools that aren't declared to you (? marks optional arguments):\n" + c.index())
	default:
		d.WriteString("\n\nEvery tool you have works this way, with the same arguments.")
	}
	return llm.ToolSpec{
		Name:        CodeToolName,
		Description: d.String(),
		Schema: schema(`{"type":"object","properties":{
			"code":{"type":"string","description":"The JavaScript to run"},
			"timeout_seconds":{"type":"integer","description":"Wall-clock limit (default 120, max 600)"}},
			"required":["code"]}`),
	}
}

// index lists the tools with their argument names, falling back to names
// only past indexBudget.
func (c codeTool) index() string {
	var lines, names []string
	for _, sp := range c.listed {
		lines = append(lines, "- "+signature(sp)+": "+clipLine(sp.Description, 90))
		names = append(names, sp.Name)
	}
	if out := strings.Join(lines, "\n"); len(out) <= indexBudget {
		return out
	}
	return strings.Join(names, ", ") + "\n(Use tools.describe(name) for arguments.)"
}

// signature renders a spec as name({required, optional?}).
func signature(sp llm.ToolSpec) string {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	_ = json.Unmarshal(sp.Schema, &s)
	req := map[string]bool{}
	for _, r := range s.Required {
		req[r] = true
	}
	props := make([]string, 0, len(s.Properties))
	for p := range s.Properties {
		props = append(props, p)
	}
	sort.Slice(props, func(i, j int) bool {
		if req[props[i]] != req[props[j]] {
			return req[props[i]]
		}
		return props[i] < props[j]
	})
	for i, p := range props {
		if !req[p] {
			props[i] = p + "?"
		}
	}
	return sp.Name + "({" + strings.Join(props, ", ") + "})"
}

func (c codeTool) find(name string) (llm.ToolSpec, bool) {
	for _, sp := range c.specs {
		if sp.Name == name {
			return sp, true
		}
	}
	return llm.ToolSpec{}, false
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
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	s := &script{vm: goja.New(), spec: c, caller: caller, ctx: ctx}
	s.install()
	stop := context.AfterFunc(ctx, func() { s.vm.Interrupt(ctx.Err()) })
	defer stop()

	v, err := s.run(in.Code)
	out := s.out.String()
	if s.truncated {
		out += "\n[output stopped at 1 MB]"
	}
	if err == nil && v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) {
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += "=> " + s.show(v)
	}
	footer := fmt.Sprintf("[%s: %d tool call%s]", CodeToolName, s.calls, plural(s.calls))
	if err != nil {
		msg := scriptError(err, ctx, timeout)
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

// scriptError explains why a script stopped.
func scriptError(err error, ctx context.Context, timeout time.Duration) string {
	var intr *goja.InterruptedError
	if errors.As(err, &intr) {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Sprintf("Error: the script timed out after %s", timeout)
		}
		return "Error: interrupted by user"
	}
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
	return err.Error()
}

// script is one run_code execution.
type script struct {
	vm        *goja.Runtime
	spec      codeTool
	caller    Caller
	ctx       context.Context
	out       strings.Builder
	truncated bool
	calls     int
}

func (s *script) install() {
	console := s.vm.NewObject()
	for _, name := range []string{"log", "info", "warn", "error", "debug"} {
		_ = console.Set(name, s.print)
	}
	_ = s.vm.Set("console", console)
	_ = s.vm.Set("print", s.print)

	t := s.vm.NewObject()
	for _, sp := range s.spec.specs {
		name := sp.Name
		_ = t.Set(name, func(call goja.FunctionCall) goja.Value { return s.invoke(name, call.Argument(0)) })
	}
	_ = t.Set("call", func(name string, args goja.Value) goja.Value { return s.invoke(name, args) })
	_ = t.Set("describe", func(name string) goja.Value {
		sp, ok := s.spec.find(name)
		if !ok {
			s.throw("no tool named %q; tools.list() returns the names", name)
		}
		var sch any
		_ = json.Unmarshal(sp.Schema, &sch)
		return s.vm.ToValue(map[string]any{"name": sp.Name, "description": sp.Description, "schema": sch})
	})
	_ = t.Set("list", func() []string {
		names := make([]string, len(s.spec.specs))
		for i, sp := range s.spec.specs {
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

func (s *script) invoke(name string, args goja.Value) goja.Value {
	if _, ok := s.spec.find(name); !ok {
		s.throw("no tool named %q is available to scripts; tools.list() returns the names", name)
	}
	if s.calls >= codeMaxCalls {
		s.throw("the script reached its limit of %d tool calls", codeMaxCalls)
	}
	s.calls++
	input := json.RawMessage(`{}`)
	if args != nil && !goja.IsUndefined(args) && !goja.IsNull(args) {
		b, err := json.Marshal(args.Export())
		if err != nil {
			s.throw("%s: arguments aren't JSON: %v", name, err)
		}
		input = b
	}
	res := s.caller.CallTool(s.ctx, name, input)
	if err := s.ctx.Err(); err != nil {
		s.vm.Interrupt(err)
	}
	content := res.Content
	if n := len(res.Images); n > 0 {
		content += fmt.Sprintf("\n[%d image(s) omitted: scripts get text only]", n)
	}
	if res.IsError {
		s.throw("%s: %s", name, content)
	}
	return s.vm.ToValue(content)
}

func (s *script) print(call goja.FunctionCall) goja.Value {
	parts := make([]string, len(call.Arguments))
	for i, a := range call.Arguments {
		parts[i] = s.show(a)
	}
	line := strings.Join(parts, " ") + "\n"
	if room := codeMaxBuffer - s.out.Len(); len(line) > room {
		line, s.truncated = safeCut(line, max(room, 0)), true
	}
	s.out.WriteString(line)
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
