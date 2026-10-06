package agent

// Origin says which agent an observed event came from.
type Origin struct {
	// Agent labels a subagent ("explore: find auth"); empty for the main
	// agent.
	Agent string
	// Parent is the task call that started the subagent.
	Parent string
}

// Observer receives the events every agent emits, for telemetry. It is
// called on the emitting agent's goroutine, so it must return quickly and
// must not call back into the agent. Streaming deltas are not delivered.
// Events travel the same way to the front end, so an observer sees what a
// client sees and nothing more.
type Observer interface {
	Observe(Origin, Event)
}

// observe passes one of this agent's own events to the observer.
func (a *Agent) observe(e Event) {
	if a.opts.Observer == nil {
		return
	}
	switch e.Kind {
	case EvTextDelta, EvThinkingDelta, EvToolCallDelta:
		return
	}
	a.opts.Observer.Observe(a.origin, e)
}
