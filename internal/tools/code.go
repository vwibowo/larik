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
	// ExecTools offers the tools only.
	ExecTools Execution = "tools"
	// ExecHybrid offers the tools and run_code; the model picks per step.
	ExecHybrid Execution = "hybrid"
	// ExecCode offers run_code and the conversation-level tools (see
	// directInCode). Ordinary tools are reached from scripts.
	ExecCode Execution = "code"
)

// DefaultExecution is the execution of a model with no setting of its own.
const DefaultExecution = ExecCode

func ParseExecution(s string) (Execution, error) {
	switch e := Execution(s); e {
	case ExecTools, ExecHybrid, ExecCode:
		return e, nil
	case "":
		return DefaultExecution, nil
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
	// codeMaxOutput bounds what a script may print. The model sees its
	// start and end within MaxOutputBytes; raw_output reads the rest.
	codeMaxOutput = 16 << 20
	// codeMaxValue bounds the rendering of the script's final value.
	codeMaxValue = 1 << 20
	// scriptBashOutput bounds the output a script gets from bash, which
	// is more than the model sees so that scripts can filter it; read,
	// grep and glob give scripts more room for the same reason.
	scriptBashOutput    = 1 << 20
	scriptReadBytes     = 1 << 20
	scriptSearchResults = 5000
	scriptSearchBytes   = 1 << 20
	codeStateKeyMax     = 256
	codeStateValueMax   = 256 << 10
	codeStateTotalMax   = 1 << 20
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

type userWaitKey struct{}

// WithUserWait sets what WaitingForUser runs: wait is called when a
// delegated call starts waiting for the user, and returns the function
// that ends the wait. run_code uses it to stop its time limit meanwhile.
func WithUserWait(ctx context.Context, wait func() (end func())) context.Context {
	return context.WithValue(ctx, userWaitKey{}, wait)
}

// WaitingForUser marks the start of a wait for the user, such as a
// permission prompt; call the returned function when it ends.
func WaitingForUser(ctx context.Context) (end func()) {
	if wait, ok := ctx.Value(userWaitKey{}).(func() func()); ok {
		return wait()
	}
	return func() {}
}

// Call is one delegated tool call.
type Call struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// BatchCaller is a Caller that can also run several calls at once: they
// are authorized in order, then the ones that may run in parallel (see
// Parallel) do. Results come back in the order of calls.
type BatchCaller interface {
	Caller
	CallTools(ctx context.Context, calls []Call) []Result
}

// CodeStateChange is one successful script's transactional store changes.
type CodeStateChange struct {
	Set    map[string]json.RawMessage
	Delete []string
}

// StateCaller gives run_code branch-local state without coupling tools to
// session persistence. Commit must apply all changes or none.
type StateCaller interface {
	CodeState() map[string]json.RawMessage
	CommitCodeState(CodeStateChange) error
}

func validateCodeState(state map[string]json.RawMessage, change CodeStateChange) error {
	next := make(map[string]json.RawMessage, len(state)+len(change.Set))
	for key, value := range state {
		next[key] = value
	}
	for key, value := range change.Set {
		if key == "" || len(key) > codeStateKeyMax {
			return fmt.Errorf("invalid key length for %q", key)
		}
		if len(value) > codeStateValueMax || !json.Valid(value) {
			return fmt.Errorf("invalid JSON value for %q", key)
		}
		next[key] = value
	}
	for _, key := range change.Delete {
		if key == "" || len(key) > codeStateKeyMax {
			return fmt.Errorf("invalid delete key length for %q", key)
		}
		delete(next, key)
	}
	total := 0
	for _, value := range next {
		total += len(value)
	}
	if total > codeStateTotalMax {
		return fmt.Errorf("values total %d bytes, over the %d-byte limit", total, codeStateTotalMax)
	}
	return nil
}

type scriptKey struct{}

// InScript reports whether ctx runs a call a run_code script made, so a
// tool can give the script more than the model would see (see Result.Data).
func InScript(ctx context.Context) bool {
	v, _ := ctx.Value(scriptKey{}).(bool)
	return v
}

// OutsideScript returns ctx for calls a model makes itself, such as a
// subagent's started by a script, so they get what the model would see.
func OutsideScript(ctx context.Context) context.Context {
	if !InScript(ctx) {
		return ctx
	}
	return context.WithValue(ctx, scriptKey{}, false)
}

// jsName is the name a tool has in scripts: its own when that is a
// JavaScript identifier, otherwise with the other characters replaced by _.
func jsName(name string) string {
	b := []byte(name)
	for i, c := range b {
		if !(c == '_' || c == '$' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' && i > 0) {
			b[i] = '_'
		}
	}
	return string(b)
}

// toolsMethods are the tools object's own functions, which no tool may shadow.
var toolsMethods = map[string]bool{"call": true, "describe": true, "list": true, "parallel": true, "search": true}

// scriptNames maps each spec's name to the property it is bound to on the
// tools object: its jsName, unless that is taken (by another tool, an
// earlier alias or a method), in which case its own name. specs are
// sorted, so the parent's index and the child's bindings agree.
func scriptNames(specs []llm.ToolSpec) map[string]string {
	taken := map[string]bool{}
	for _, sp := range specs {
		taken[sp.Name] = true
	}
	names := make(map[string]string, len(specs))
	for _, sp := range specs {
		names[sp.Name] = sp.Name
		if js := jsName(sp.Name); js != sp.Name && !taken[js] && !toolsMethods[js] {
			taken[js] = true
			names[sp.Name] = js
		}
	}
	return names
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
	// Conversation-level tools are steps of the conversation, not of a
	// script, so scripts cannot call them; the model still needs them
	// directly: subagents for delegation, the task list, skills, memory
	// and plan approval.
	for _, name := range directInCode {
		if tool, ok := r.Get(name); ok {
			modelTools = append(modelTools, tool)
		}
	}
	return NewRegistry(modelTools...)
}

// ScriptName is what a script calls the named tool by on the tools
// object, and whether scripts can call it at all.
func (r *Registry) ScriptName(name string) (string, bool) {
	js, ok := scriptNames(r.bindable())[name]
	return js, ok
}

// directInCode are the tools the model calls directly under ExecCode.
var directInCode = []string{"task", "task_wait", "task_stop", TodoToolName, "skill", "memory", "exit_plan_mode"}

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
	fmt.Fprintf(&d, "tools.<name>(args) runs a tool with the arguments of its schema and returns its result, or throws an Error with the tool's message; tools.call(name, args) is the same. "+
		"Most tools return their text output; these return objects instead, with more room than a direct call: "+scriptReturnsText()+". "+
		"tools.bash doesn't throw for a non-zero exit code. "+
		"tools.parallel([{name, args}, ...]) runs several calls at once (read-only ones side by side) and returns [{ok: true, value} or {ok: false, error}] in order. "+
		"In names, characters a JavaScript identifier can't have become _ (mcp__my-server__x is tools.mcp__my_server__x). "+
		"store(key, value) and load(key) keep small JSON values across successful scripts in this session; store(key, undefined) deletes. Changes commit only when the script succeeds. "+
		"tools.search(query, limit?) ranks the tools by keywords and returns [{name, signature, description}]; tools.describe(name) returns a tool's schema, tools.list() the names. console.log prints, and the last expression's value is printed too. "+
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

// scriptReturns are the values tools give scripts in place of their text
// (Result.Data), by tool.
var scriptReturns = map[string]string{
	"read": "{path, content, start_line, end_line, more, next_offset?} (content is the lines as they are, without line numbers)",
	"grep": "{matches: [{path, line, text}], truncated}",
	"glob": "{paths, truncated}",
	"bash": "{output, exit_code, truncated, raw_output_id?}",
}

func scriptReturnsText() string {
	var parts []string
	for _, name := range []string{"read", "grep", "glob", "bash"} {
		parts = append(parts, "tools."+name+" → "+scriptReturns[name])
	}
	return strings.Join(parts, "; ")
}

// coreTools are described in full in code execution's index, with their
// arguments; the rest get a line each. Scripts lean on these the most,
// and their rules (edit's unique old_string, read's limits) matter.
var coreTools = map[string]bool{"read": true, "write": true, "edit": true, "multi_edit": true, "bash": true, "grep": true, "glob": true, "raw_output": true}

// index lists the tools: the core ones in full, the others with their
// argument names, falling back to names only past indexBudget.
func (c codeTool) index() string {
	bound := scriptNames(c.specs)
	var core, lines, names []string
	for _, sp := range c.listed {
		name := bound[sp.Name]
		if coreTools[sp.Name] {
			core = append(core, c.fullEntry(name, sp))
			continue
		}
		lines = append(lines, "- "+signature(name, sp)+": "+indexLine(sp))
		names = append(names, name)
	}
	rest := strings.Join(lines, "\n")
	if len(rest) > indexBudget {
		rest = strings.Join(names, ", ") + "\n(Use tools.search(query) to find one, tools.describe(name) for its arguments.)"
	}
	return strings.TrimSpace(strings.Join(core, "\n") + "\n" + rest)
}

// fullEntry is a core tool's index entry: its signature and what it
// returns, its description, and what each argument means.
func (c codeTool) fullEntry(name string, sp llm.ToolSpec) string {
	head := "- " + signature(name, sp)
	if r := scriptReturns[sp.Name]; r != "" {
		head += " → " + strings.SplitN(r, " (", 2)[0]
	}
	out := head + "\n  " + scriptDescription(sp.Description)
	if args := argNotes(sp); args != "" {
		out += "\n  " + args
	}
	return out
}

// scriptDescription drops the sentences of a description that point to
// the wrappers scripts replace, such as write's to write_files.
func scriptDescription(desc string) string {
	sentences := strings.SplitAfter(strings.Join(strings.Fields(desc), " "), ". ")
	var kept []string
	for _, s := range sentences {
		if !strings.Contains(s, WriteFilesToolName) && !strings.Contains(s, CallToolName) && !strings.Contains(s, ToolSearchName) {
			kept = append(kept, s)
		}
	}
	return strings.TrimSpace(strings.Join(kept, ""))
}

// argNotes are the descriptions of a tool's arguments, as "name: what".
func argNotes(sp llm.ToolSpec) string {
	var s struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	_ = json.Unmarshal(sp.Schema, &s)
	names := make([]string, 0, len(s.Properties))
	for p, v := range s.Properties {
		if v.Description != "" {
			names = append(names, p)
		}
	}
	sort.Strings(names)
	notes := make([]string, len(names))
	for i, p := range names {
		notes[i] = p + ": " + strings.Join(strings.Fields(s.Properties[p].Description), " ")
	}
	return strings.Join(notes, "; ")
}

// signature renders a spec as name({required, optional?}), with the shape
// of an argument that is a list of objects.
func signature(name string, sp llm.ToolSpec) string {
	return name + "(" + objectShape(sp.Schema) + ")"
}

// objectShape renders an object schema as {required, optional?}; a
// property that is an array of objects shows its items' shape.
func objectShape(schema json.RawMessage) string {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	_ = json.Unmarshal(schema, &s)
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
	out := make([]string, len(props))
	for i, p := range props {
		out[i] = p
		if !req[p] {
			out[i] += "?"
		}
		var prop struct {
			Items json.RawMessage `json:"items"`
		}
		var items struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if json.Unmarshal(s.Properties[p], &prop) == nil && json.Unmarshal(prop.Items, &items) == nil && len(items.Properties) > 0 {
			out[i] += ": [" + objectShape(prop.Items) + "]"
		}
	}
	return "{" + strings.Join(out, ", ") + "}"
}
