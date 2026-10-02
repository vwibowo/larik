package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

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
	// ExecCode offers run_code and the plan approval tool, when present.
	// Ordinary tools are reached from scripts.
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
	// codeMaxStack bounds the script's call depth, so runaway recursion
	// stops with an error instead of growing until the memory limit.
	codeMaxStack = 10_000
	// codeMaxInput bounds the arguments of one tool call from a script.
	codeMaxInput = 16 << 20
)

// codeMemoryLimit is the heap a script's process may use (a variable so
// tests can lower it).
var codeMemoryLimit uint64 = 256 << 20

// unbound are tools a script can't call: the wrappers it replaces, itself,
// and tools that only make sense as a step of the conversation.
var unbound = map[string]bool{
	CodeToolName: true, WriteFilesToolName: true, ToolSearchName: true, CallToolName: true,
	"todo_write": true, "task": true, "task_stop": true, "task_wait": true,
	"exit_plan_mode": true, "skill": true, "memory": true,
}

// Caller runs one delegated tool call, with the same
// permission checks, hooks and events as a call the model makes directly.
type Caller interface {
	CallTool(ctx context.Context, name string, input json.RawMessage) Result
}

type callerKey struct{}

// WithCaller lets tools that delegate calls (run_code, write_files) reach the agent.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFrom returns the Caller set with WithCaller.
func CallerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}

// ForExecution returns the registry the model sees under e. Hybrid adds
// run_code; code declares run_code and, when present, exit_plan_mode.
// Script calls resolve against the base registry instead.
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
	modelTools := []Tool{codeTool{specs: specs, listed: specs}}
	if plan, ok := r.Get("exit_plan_mode"); ok {
		modelTools = append(modelTools, plan)
	}
	return NewRegistry(modelTools...)
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
		"Print summaries, not raw dumps. Limits per script: %d tool calls, %d MB of memory, and %s (timeout_seconds, up to %d).",
		codeMaxCalls, codeMemoryLimit>>20, codeTimeoutDefault, int(codeTimeoutMax.Seconds()))
	switch {
	case !c.hybrid:
		d.WriteString("\n\nTools (? marks optional arguments):\n" + c.index())
	case len(c.listed) > 0:
		// tool_search's description already lists them; repeating the
		// list here would send it twice on every request.
		d.WriteString("\n\nEvery tool you have works this way, with the same arguments, and so do the tools listed in " + ToolSearchName + "'s description; tools.describe(name) gives one's arguments.")
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
		lines = append(lines, "- "+signature(sp)+": "+indexLine(sp))
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
