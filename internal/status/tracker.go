package status

import (
	"slices"
	"sync"
)

// Tracker derives a session's status from what its front end tells it and
// keeps the status file in step. The server and headless mode use it; the
// TUI derives its status from its own state.
type Tracker struct {
	w *Writer

	mu          sync.Mutex
	busy        bool
	waits       []wait // open requests, oldest first
	ended       bool   // a turn has finished since the session opened
	failed      bool   // the main agent reported an error this turn
	interrupted bool   // the last turn was interrupted
}

type wait struct {
	id   string
	kind Kind
	msg  string
}

// NewTracker reports through w, which may be nil to track without writing.
// A nil *Tracker ignores every call.
func NewTracker(w *Writer) *Tracker { return &Tracker{w: w} }

// Busy marks a run starting or ending. A run starting forgets how the
// last one ended.
func (t *Tracker) Busy(busy bool) {
	t.update(func() {
		if busy && !t.busy {
			t.failed, t.interrupted = false, false
		}
		if !busy && t.busy {
			t.ended = true
		}
		t.busy = busy
	})
}

// Block records a request waiting on the user, under an id Unblock takes.
func (t *Tracker) Block(id string, kind Kind, msg string) {
	t.update(func() { t.waits = append(t.waits, wait{id, kind, msg}) })
}

// Unblock drops an answered or expired request.
func (t *Tracker) Unblock(id string) {
	t.update(func() {
		t.waits = slices.DeleteFunc(t.waits, func(w wait) bool { return w.id == id })
	})
}

// Failed records that the main agent reported an error this turn.
func (t *Tracker) Failed() { t.update(func() { t.failed = true }) }

// Stopped records the main agent's stop reason for the turn.
func (t *Tracker) Stopped(reason string) {
	t.update(func() { t.interrupted = reason == "interrupted" })
}

// Report is the status as of now.
func (t *Tracker) Report() Report {
	if t == nil {
		return Report{State: Idle}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.report()
}

// Release says this process no longer has the session open.
func (t *Tracker) Release() {
	if t != nil {
		t.w.Release()
	}
}

func (t *Tracker) update(fn func()) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	fn()
	t.w.Set(t.report())
}

func (t *Tracker) report() Report {
	switch {
	case len(t.waits) > 0:
		w := t.waits[0]
		return Report{State: Blocked, Kind: w.kind, Msg: w.msg}
	case t.busy:
		return Report{State: Working}
	case !t.ended || t.interrupted:
		return Report{State: Idle}
	case t.failed:
		return Report{State: Error}
	}
	return Report{State: Done}
}
