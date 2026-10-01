package agent

import (
	"cmp"
	"context"
	"slices"

	"larik/internal/llm"
	"larik/internal/session"
	"larik/internal/tools"
)

type runCtxKey struct{}

type runCtx struct {
	agent *Agent
	emit  func(Event)
}

// withRun lets a tool reach the agent running it and its event stream.
func withRun(ctx context.Context, a *Agent, emit func(Event)) context.Context {
	return context.WithValue(ctx, runCtxKey{}, runCtx{a, emit})
}

// FromContext returns the agent executing the current tool call and a
// function that emits events into its stream.
func FromContext(ctx context.Context) (*Agent, func(Event), bool) {
	rc, ok := ctx.Value(runCtxKey{}).(runCtx)
	return rc.agent, rc.emit, ok
}

type SpawnOptions struct {
	Type     string // agent type, e.g. "explore"
	Provider llm.Provider
	Model    string
	System   string
	Tools    *tools.Registry
	Session  *session.Session // optional transcript for the child
	MaxTurns int

	// Cwd, when set, runs the child in another directory, such as its own
	// git worktree: paths and path rules resolve there, bash uses Sandbox
	// (which should be confined to that directory), and its file changes
	// are neither checkpointed nor sent to the parent's language servers,
	// which belong to the parent's tree.
	Cwd     string
	Sandbox tools.Sandbox

	// Label names the child in a debug trace, and TraceParent is the task
	// call that started it.
	Label, TraceParent string
}

// Spawn creates a subagent that shares this agent's permissions (and mode),
// hooks and checkpoints, but has its own context, prompt and tools.
func (a *Agent) Spawn(o SpawnOptions) *Agent {
	a.mu.Lock()
	effort, noAutoCompact, compactWith, tr, exec, execFor := a.opts.Effort, a.opts.NoAutoCompact, a.opts.CompactWith, a.opts.Trace, a.opts.Execution, a.opts.ExecutionFor
	a.mu.Unlock()
	// A subagent on another model gets that model's execution.
	if execFor != nil && o.Provider != nil {
		exec = execFor(o.Provider.Name(), o.Model)
	}
	label := o.Label
	if label == "" {
		label = o.Type
	}
	var runtime llm.AgentRuntime
	if p, ok := o.Provider.(interface{ AgentRuntime() llm.AgentRuntime }); ok {
		runtime = p.AgentRuntime()
	}
	opts := Options{
		Provider:      o.Provider,
		Runtime:       runtime,
		Model:         o.Model,
		Effort:        effort,
		NoAutoCompact: noAutoCompact,
		CompactWith:   compactWith,
		System:        o.System,
		Cwd:           a.opts.Cwd,
		MaxTurns:      o.MaxTurns,
		Tools:         o.Tools,
		Execution:     exec,
		ExecutionFor:  execFor,
		Perms:         a.opts.Perms,
		Session:       o.Session,
		Checkpoints:   a.opts.Checkpoints,
		OnAllowRule:   a.opts.OnAllowRule,
		AutoApprove:   a.autoApprover(),
		Hooks:         a.opts.Hooks,
		LSP:           a.opts.LSP,
		Sandbox:       a.opts.Sandbox,
		Subagent:      o.Type,
		Trace:         tr.Child(label, o.TraceParent),
	}
	if o.Cwd != "" && o.Cwd != a.opts.Cwd {
		opts.Cwd = o.Cwd
		if opts.Perms != nil {
			opts.Perms = opts.Perms.WithCwd(o.Cwd)
		}
		opts.Sandbox = o.Sandbox
		opts.Checkpoints, opts.LSP = nil, nil
	}
	child := New(opts)
	child.parent = a
	child.owner = llm.NewCallID(label + "-")
	child.tokenSaver = a.tokenSaver
	child.env.TokenSaver = a.tokenSaver
	child.env.RawOutputDir = a.env.RawOutputDir
	return child
}

// AddUsage adds spend made on this agent's behalf (e.g. by a subagent) to
// its totals and records it in the session.
func (a *Agent) AddUsage(model string, u llm.Usage) {
	a.mu.Lock()
	a.usage.Add(u)
	a.cost += llm.Lookup(model).Cost(u)
	a.addModelUsageLocked(model, u)
	a.mu.Unlock()
	if a.opts.Session != nil {
		a.saveFailed(a.opts.Session.AppendUsage(model, u))
	}
}

// ModelSpend is one model's share of the session's usage.
type ModelSpend struct {
	Model   string
	Usage   llm.Usage
	CostUSD float64
	Priced  bool // the catalog has a price for the model
}

func (a *Agent) addModelUsageLocked(model string, u llm.Usage) {
	if a.byModel == nil {
		a.byModel = map[string]llm.Usage{}
	}
	mu := a.byModel[model]
	mu.Add(u)
	a.byModel[model] = mu
}

// SpendByModel breaks the session's usage down by model, costliest first.
func (a *Agent) SpendByModel() []ModelSpend {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]ModelSpend, 0, len(a.byModel))
	for m, u := range a.byModel {
		info := llm.Lookup(m)
		out = append(out, ModelSpend{Model: m, Usage: u, CostUSD: info.Cost(u), Priced: info.InputPrice+info.OutputPrice > 0})
	}
	slices.SortFunc(out, func(x, y ModelSpend) int {
		if x.CostUSD != y.CostUSD {
			return cmp.Compare(y.CostUSD, x.CostUSD)
		}
		return cmp.Compare(x.Model, y.Model)
	})
	return out
}

// Owner identifies this agent to tools that keep per-agent state: "" for
// the main agent, a unique id for a subagent.
func (a *Agent) Owner() string { return a.owner }
