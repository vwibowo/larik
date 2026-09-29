package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// MaxBackground bounds concurrently running background tasks.
const MaxBackground = 8

type BgStatus string

const (
	BgRunning   BgStatus = "running"
	BgCompleted BgStatus = "completed"
	BgFailed    BgStatus = "failed"
	BgStopped   BgStatus = "stopped"
)

// BgTask is a snapshot of a background task.
type BgTask struct {
	ID       string
	Label    string
	Status   BgStatus
	Result   string
	Started  time.Time
	Finished time.Time
}

type bgTask struct {
	BgTask
	cancel    context.CancelFunc
	done      chan struct{}
	delivered bool // result already given to the model
}

// background runs tasks that outlive the turn that started them. Their
// events (and a final EvTaskDone) go to a long-lived channel, because a
// turn's own event channel closes when the turn ends.
type background struct {
	mu     sync.Mutex
	seq    int
	tasks  map[string]*bgTask
	events chan Event
	ctx    context.Context
	cancel context.CancelFunc
}

func (a *Agent) hub() *background {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.bg == nil {
		ctx, cancel := context.WithCancel(context.Background())
		a.bg = &background{tasks: map[string]*bgTask{}, events: make(chan Event, 256), ctx: ctx, cancel: cancel}
	}
	return a.bg
}

// Background returns the stream of background-task events. Front ends
// should read it for the agent's whole lifetime and answer EvPermission.
func (a *Agent) Background() <-chan Event { return a.hub().events }

func (h *background) emit(e Event) {
	select {
	case h.events <- e:
	case <-h.ctx.Done():
	}
}

// StartBackground runs fn in the background and returns its task id.
// fn receives a context that is cancelled by StopBackground or shutdown,
// and an emit func for forwarding progress events.
func (a *Agent) StartBackground(label string, fn func(ctx context.Context, emit func(Event)) (result string, failed bool)) (string, error) {
	h := a.hub()
	h.mu.Lock()
	running := 0
	for _, t := range h.tasks {
		if t.Status == BgRunning {
			running++
		}
	}
	if running >= MaxBackground {
		h.mu.Unlock()
		return "", fmt.Errorf("too many background tasks running (%d); wait for some to finish", running)
	}
	h.seq++
	id := fmt.Sprintf("bg-%d", h.seq)
	ctx, cancel := context.WithCancel(h.ctx)
	t := &bgTask{BgTask: BgTask{ID: id, Label: label, Status: BgRunning, Started: time.Now()}, cancel: cancel, done: make(chan struct{})}
	h.tasks[id] = t
	h.mu.Unlock()

	go func() {
		result, failed := fn(ctx, h.emit)
		h.mu.Lock()
		switch {
		case t.Status == BgStopped:
		case failed:
			t.Status = BgFailed
		default:
			t.Status = BgCompleted
		}
		if t.Status == BgStopped && result == "" {
			result = "stopped before finishing"
		}
		t.Result, t.Finished = result, time.Now()
		snap := t.BgTask
		h.mu.Unlock()
		cancel()
		close(t.done)
		h.emit(Event{Kind: EvTaskDone, ToolID: snap.ID, Agent: snap.Label, Output: snap.Result, IsError: snap.Status != BgCompleted, StopReason: string(snap.Status)})
	}()
	return id, nil
}

// StopBackground cancels a running task.
func (a *Agent) StopBackground(id string) error {
	h := a.hub()
	h.mu.Lock()
	t, ok := h.tasks[id]
	if !ok {
		h.mu.Unlock()
		return fmt.Errorf("no background task %q", id)
	}
	if t.Status != BgRunning {
		h.mu.Unlock()
		return fmt.Errorf("%s already %s", id, t.Status)
	}
	t.Status = BgStopped
	t.delivered = true // nothing useful to report back unprompted
	h.mu.Unlock()
	t.cancel()
	select {
	case <-t.done:
	case <-time.After(stopWait):
		// It is marked stopped and its context is canceled; don't hold the
		// caller while a stuck tool winds down.
	}
	return nil
}

// stopWait bounds how long StopBackground waits for a task to exit.
const stopWait = 10 * time.Second

// StopAllBackground cancels every task and waits briefly for them to exit.
func (a *Agent) StopAllBackground() {
	a.mu.Lock()
	h := a.bg
	a.mu.Unlock()
	if h == nil {
		return
	}
	h.mu.Lock()
	var waits []chan struct{}
	for _, t := range h.tasks {
		if t.Status == BgRunning {
			t.Status, t.delivered = BgStopped, true
			waits = append(waits, t.done)
		}
	}
	h.mu.Unlock()
	h.cancel()
	deadline := time.After(3 * time.Second)
	for _, w := range waits {
		select {
		case <-w:
		case <-deadline:
			return
		}
	}
}

// BackgroundTasks lists tasks, newest last.
func (a *Agent) BackgroundTasks() []BgTask {
	h := a.hub()
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]BgTask, 0, len(h.tasks))
	for _, t := range h.tasks {
		out = append(out, t.BgTask)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

// RunningBackground counts tasks still running.
func (a *Agent) RunningBackground() int {
	n := 0
	for _, t := range a.BackgroundTasks() {
		if t.Status == BgRunning {
			n++
		}
	}
	return n
}

// PendingNotifications counts finished tasks whose results the model hasn't seen.
func (a *Agent) PendingNotifications() int {
	h := a.hub()
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, t := range h.tasks {
		if t.Status != BgRunning && !t.delivered {
			n++
		}
	}
	return n
}

// takeNotifications formats undelivered results and marks them delivered.
func (a *Agent) takeNotifications() string {
	a.mu.Lock()
	h := a.bg
	a.mu.Unlock()
	if h == nil {
		return ""
	}
	h.mu.Lock()
	var ready []*bgTask
	for _, t := range h.tasks {
		if t.Status != BgRunning && !t.delivered {
			t.delivered = true
			ready = append(ready, t)
		}
	}
	h.mu.Unlock()
	if len(ready) == 0 {
		return ""
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].Finished.Before(ready[j].Finished) })
	var b strings.Builder
	for i, t := range ready {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "<task-notification id=%q agent=%q status=%q>\n%s\n</task-notification>", t.ID, t.Label, t.Status, t.Result)
	}
	return b.String()
}

// WaitBackground blocks until the given tasks (all running ones if ids is
// empty) finish or ctx ends, and returns their snapshots, marking the
// finished ones delivered.
func (a *Agent) WaitBackground(ctx context.Context, ids []string) ([]BgTask, error) {
	h := a.hub()
	h.mu.Lock()
	var targets []*bgTask
	if len(ids) == 0 {
		for _, t := range h.tasks {
			if t.Status == BgRunning || !t.delivered {
				targets = append(targets, t)
			}
		}
	} else {
		for _, id := range ids {
			t, ok := h.tasks[id]
			if !ok {
				h.mu.Unlock()
				return nil, fmt.Errorf("no background task %q", id)
			}
			targets = append(targets, t)
		}
	}
	h.mu.Unlock()

	var waitErr error
	for _, t := range targets {
		select {
		case <-t.done:
		case <-ctx.Done():
			waitErr = ctx.Err()
		}
		if waitErr != nil {
			break
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]BgTask, 0, len(targets))
	for _, t := range targets {
		if t.Status != BgRunning {
			t.delivered = true
		}
		out = append(out, t.BgTask)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	if errors.Is(waitErr, context.DeadlineExceeded) {
		waitErr = nil // a timeout just means "not done yet"
	}
	return out, waitErr
}

// RunNotifications starts a turn that delivers finished background results
// to the model. It returns false when there is nothing to deliver.
func (a *Agent) RunNotifications(ctx context.Context) (<-chan Event, bool) {
	note := a.takeNotifications()
	if note == "" {
		return nil, false
	}
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		prompt := note + "\n\nBackground task results arrived. Continue with them as appropriate; if nothing needs doing, reply briefly."
		send := func(e Event) { ch <- e }
		reason := a.runWith(ctx, prompt, true, send)
		a.reportSaveError(send)
		ch <- Event{Kind: EvDone, StopReason: reason}
	}()
	return ch, true
}
