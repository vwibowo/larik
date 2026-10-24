package agent

import "larik/internal/checkpoint"

// Subagents editing the parent's tree get an explicit undo unit. A unit
// finishing within its originating turn joins it; one outliving that turn
// stays independent, never borrowing a later foreground turn's ownership.
func (a *Agent) checkpointUnit() *checkpoint.Unit {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.undoUnit
}

func (a *Agent) beginCheckpointUnit(child *Agent, label string) {
	store := child.opts.Checkpoints
	if store == nil {
		return
	}
	var u *checkpoint.Unit
	if parent := a.checkpointUnit(); parent != nil {
		u = parent.Begin(label)
	} else {
		u = store.Begin(label)
	}
	child.undoUnit = u // Spawn has not published the child yet.
	child.env.BeforeWrite, child.env.RecordOriginal = u.Capture, u.Record
}

// EndCheckpoint finishes a subagent's undo unit after its tools stop. It
// is idempotent, is called by runWith, and does nothing for the main agent.
func (a *Agent) EndCheckpoint() {
	a.mu.Lock()
	u := a.undoUnit
	a.undoUnit = nil
	a.mu.Unlock()
	if u != nil {
		a.saveFailed(u.End())
	}
}
