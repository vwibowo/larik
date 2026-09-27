package agent

import (
	"context"

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
}

// Spawn creates a subagent that shares this agent's permissions (and mode),
// hooks and checkpoints, but has its own context, prompt and tools.
func (a *Agent) Spawn(o SpawnOptions) *Agent {
	a.mu.Lock()
	effort := a.opts.Effort
	a.mu.Unlock()
	return New(Options{
		Provider:    o.Provider,
		Model:       o.Model,
		Effort:      effort,
		System:      o.System,
		Cwd:         a.opts.Cwd,
		MaxTurns:    o.MaxTurns,
		Tools:       o.Tools,
		Perms:       a.opts.Perms,
		Session:     o.Session,
		Checkpoints: a.opts.Checkpoints,
		OnAllowRule: a.opts.OnAllowRule,
		Hooks:       a.opts.Hooks,
		LSP:         a.opts.LSP,
		Sandbox:     a.opts.Sandbox,
		Subagent:    o.Type,
	})
}

// AddUsage adds spend made on this agent's behalf (e.g. by a subagent) to
// its totals and records it in the session.
func (a *Agent) AddUsage(model string, u llm.Usage) {
	a.mu.Lock()
	a.usage.Add(u)
	a.cost += llm.Lookup(model).Cost(u)
	a.mu.Unlock()
	if a.opts.Session != nil {
		_ = a.opts.Session.AppendUsage(model, u)
	}
}
