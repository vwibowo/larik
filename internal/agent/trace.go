package agent

import (
	"larik/internal/llm"
	"larik/internal/trace"
)

// tracer is the debug-mode recorder, or nil.
func (a *Agent) tracer() *trace.Tracer { return a.trace.Load() }

// SetTrace turns debug-mode recording on (t non-nil) or off. It applies
// from the next request, which records the whole context.
func (a *Agent) SetTrace(t *trace.Tracer) {
	a.mu.Lock()
	old := a.opts.Trace
	a.opts.Trace = t
	a.trace.Store(t)
	a.mu.Unlock()
	old.State(false)
	t.State(true)
}

// Trace is the agent's tracer, or nil when it isn't recording.
func (a *Agent) Trace() *trace.Tracer { return a.tracer() }

// traceEvents records this agent's own events as they are emitted.
// Events forwarded from a subagent carry its label and are recorded by
// the subagent's tracer, so they are skipped here.
func (a *Agent) traceEvents(emit func(Event)) func(Event) {
	return func(e Event) {
		if e.Agent == "" {
			a.observe(e)
		}
		if t := a.tracer(); t != nil && e.Agent == "" {
			switch e.Kind {
			case EvToolStart:
				t.ToolStart(e.ToolID, e.ToolName, e.Input)
			case EvToolEnd:
				t.ToolEnd(e.ToolID, e.ToolName, e.Output, e.IsError)
			case EvNotice:
				t.Note(trace.KindNotice, e.Text)
			case EvError:
				t.Note(trace.KindError, e.Text)
			}
		}
		emit(e)
	}
}

func attachmentNames(blocks []llm.Block) []string {
	var names []string
	seen := map[string]bool{}
	for _, b := range blocks {
		if b.Attachment != "" && !seen[b.Attachment] {
			seen[b.Attachment] = true
			names = append(names, b.Attachment)
		}
	}
	return names
}
