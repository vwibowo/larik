package telemetry

import (
	"sync"
	"time"

	"larik/internal/agent"
)

// Observer returns the agent.Observer for one session. All of a session's
// agents, subagents included, share it, so a subagent's spans nest under
// the task call that started it.
func (e *Exporter) Observer(sessionID string) agent.Observer {
	if e == nil {
		return nil
	}
	return &observer{x: e, session: sessionID, tools: map[string]*open{}, subs: map[string]*open{}, tasks: map[string]parentRef{}}
}

// open is a span that has started and not yet ended.
type open struct {
	trace, id, parent string
	name              string
	kind              int
	start             time.Time
	attrs             map[string]any
	// Totals over the requests made under a turn or subagent span.
	requests    int
	in, out     int
	cost        float64
	fromSubtree bool
}

type observer struct {
	x       *Exporter
	session string

	mu    sync.Mutex
	turn  *open            // the main agent's current turn
	tools map[string]*open // started tool calls, by origin and call id
	subs  map[string]*open // running subagents, by the task call that started them
	// tasks remembers where finished task calls sit, so a background
	// subagent that outlives its call still nests under it.
	tasks     map[string]parentRef
	taskOrder []string
}

type parentRef struct{ trace, id string }

const maxTasksKept = 256

func (o *observer) finish(s *open, end time.Time, errored bool, extra map[string]any) {
	attrs := map[string]any{"larik.session.id": o.session}
	for k, v := range s.attrs {
		attrs[k] = v
	}
	for k, v := range extra {
		attrs[k] = v
	}
	for k, v := range attrs {
		if v == "" { // the main agent has no label
			delete(attrs, k)
		}
	}
	o.x.emit(span{trace: s.trace, id: s.id, parent: s.parent, name: s.name, kind: s.kind,
		start: s.start, end: end, attrs: attrs, errored: errored})
}

// Observe turns the events an agent emits into spans. It reads only
// metadata: names, counts, timings and flags. Event text, tool input and
// tool output are never touched.
func (o *observer) Observe(from agent.Origin, e agent.Event) {
	now := time.Now()
	o.mu.Lock()
	defer o.mu.Unlock()

	scope := o.scope(from, now)
	switch e.Kind {
	case agent.EvToolStart:
		o.tools[key(from, e.ToolID)] = &open{
			trace: scope.trace, id: randomHex(8), parent: scope.id, kind: 1, start: now,
			name:  "execute_tool " + e.ToolName,
			attrs: map[string]any{"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": e.ToolName, "larik.agent": from.Agent},
		}
	case agent.EvToolEnd:
		k := key(from, e.ToolID)
		t, ok := o.tools[k]
		if !ok {
			return
		}
		delete(o.tools, k)
		if e.ToolName == "task" && from.Agent == "" {
			o.tasks[e.ToolID] = parentRef{t.trace, t.id}
			if o.taskOrder = append(o.taskOrder, e.ToolID); len(o.taskOrder) > maxTasksKept {
				delete(o.tasks, o.taskOrder[0])
				o.taskOrder = o.taskOrder[1:]
			}
		}
		o.finish(t, now, e.IsError, map[string]any{"larik.tool.error": e.IsError})
	case agent.EvUsage:
		if e.Usage == nil {
			return
		}
		u := e.Usage
		start := now.Add(-time.Duration(u.RequestMS) * time.Millisecond)
		if start.Before(scope.start) {
			scope.start = start
		}
		input := u.Turn.Input + u.Turn.CacheRead + u.Turn.CacheWrite
		scope.requests++
		scope.in += input
		scope.out += u.Turn.Output
		scope.cost += u.CostUSD
		o.finish(&open{trace: scope.trace, id: randomHex(8), parent: scope.id, kind: 3, start: start,
			name: "chat " + u.Model,
			attrs: map[string]any{
				"gen_ai.operation.name":      "chat",
				"gen_ai.request.model":       u.Model,
				"gen_ai.usage.input_tokens":  input,
				"gen_ai.usage.output_tokens": u.Turn.Output,
				"larik.usage.cache_read":     u.Turn.CacheRead,
				"larik.usage.cache_write":    u.Turn.CacheWrite,
				"larik.cost_usd":             u.CostUSD,
				"larik.ttft_ms":              u.TTFTMS,
				"larik.context.tokens":       u.ContextTokens,
				"larik.context.window":       u.ContextWindow,
				"larik.agent":                from.Agent,
			}}, now, false, nil)
	case agent.EvCompacted:
		if e.Compaction == nil {
			return
		}
		c := e.Compaction
		o.finish(&open{trace: scope.trace, id: randomHex(8), parent: scope.id, kind: 1, start: now, name: "larik.compaction",
			attrs: map[string]any{"larik.compaction.trigger": c.Trigger, "larik.compaction.before_tokens": c.BeforeTokens,
				"larik.compaction.after_tokens": c.AfterTokens, "larik.compaction.saved_tokens": c.SavedTokens,
				"larik.compaction.measured": c.Available, "larik.agent": from.Agent}}, now, false, nil)
	case agent.EvDone:
		o.endScope(from, scope, now, e.StopReason)
	}
}

// scope is the span events of this origin belong under: the main agent's
// turn, or a subagent's span. It is created on the first event, since
// neither a turn's start nor a subagent's has an event of its own.
func (o *observer) scope(from agent.Origin, now time.Time) *open {
	if from.Agent == "" {
		if o.turn == nil {
			o.turn = &open{trace: randomHex(16), id: randomHex(8), kind: 1, start: now, name: "larik.turn", attrs: map[string]any{}}
		}
		return o.turn
	}
	k := from.Parent
	if k == "" {
		k = from.Agent
	}
	if s, ok := o.subs[k]; ok {
		return s
	}
	s := &open{id: randomHex(8), kind: 1, start: now, name: "larik.subagent", attrs: map[string]any{"larik.agent": from.Agent}}
	if task, ok := o.tools["|"+from.Parent]; ok { // nest under the task call that started it
		s.trace, s.parent = task.trace, task.id
	} else if ref, ok := o.tasks[from.Parent]; ok { // a background task that outlived its call
		s.trace, s.parent = ref.trace, ref.id
	} else if o.turn != nil {
		s.trace, s.parent = o.turn.trace, o.turn.id
	} else {
		s.trace = randomHex(16)
	}
	o.subs[k] = s
	return s
}

func (o *observer) endScope(from agent.Origin, s *open, now time.Time, stop string) {
	extra := map[string]any{
		"larik.stop_reason": stop, "larik.requests": s.requests,
		"gen_ai.usage.input_tokens": s.in, "gen_ai.usage.output_tokens": s.out, "larik.cost_usd": s.cost,
	}
	if from.Agent == "" {
		// A turn that ends while tool calls are still open (an interrupt)
		// closes them, so no span is left dangling.
		for k, t := range o.tools {
			if len(k) > 0 && k[0] == '|' {
				delete(o.tools, k)
				o.finish(t, now, true, map[string]any{"larik.tool.error": true})
			}
		}
		o.turn = nil
		o.finish(s, now, isFailure(stop), extra)
		return
	}
	k := from.Parent
	if k == "" {
		k = from.Agent
	}
	delete(o.subs, k)
	for tk, t := range o.tools {
		if len(tk) > len(k) && tk[:len(k)+1] == k+"|" {
			delete(o.tools, tk)
			o.finish(t, now, true, map[string]any{"larik.tool.error": true})
		}
	}
	o.finish(s, now, isFailure(stop), extra)
}

// isFailure reports whether a stop reason means the turn did not finish
// what it was asked to.
func isFailure(stop string) bool {
	switch stop {
	case "error", "loop", "budget", "max_turns", "refusal":
		return true
	}
	return false
}

// key identifies a tool call: its origin's task call, then its id. The main
// agent's calls have an empty origin, so "|id".
func key(from agent.Origin, toolID string) string {
	k := from.Parent
	if k == "" {
		k = from.Agent
	}
	return k + "|" + toolID
}
