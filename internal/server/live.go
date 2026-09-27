package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"larik/internal/agent"
	"larik/internal/app"
)

var errBusy = errors.New("session is busy; wait for the current run or cancel it")

// live is a loaded session: its agent, event bus and run state.
type live struct {
	id  string
	s   *app.Session
	a   *agent.Agent
	bus *bus

	mu     sync.Mutex
	busy   bool
	closed bool
	cancel context.CancelFunc // current run
	runs   sync.WaitGroup

	pmu     sync.Mutex
	pseq    int
	pending map[string]*pendingPerm

	stopDrain chan struct{}
	drained   chan struct{}
}

type pendingPerm struct {
	ev    Event
	reply chan<- agent.PermissionReply
	bg    bool // from a background task (outlives the run)
}

func newLive(s *app.Session) *live {
	l := &live{
		id:        s.Agent.SessionID(),
		s:         s,
		a:         s.Agent,
		bus:       newBus(),
		pending:   map[string]*pendingPerm{},
		stopDrain: make(chan struct{}),
		drained:   make(chan struct{}),
	}
	go l.drainBackground()
	return l
}

// drainBackground forwards background-task events for the session's
// lifetime and starts a turn to deliver finished results when idle.
func (l *live) drainBackground() {
	defer close(l.drained)
	for {
		select {
		case e := <-l.a.Background():
			l.publish(e, true)
			if e.Kind == agent.EvTaskDone {
				l.deliverNotifications()
			}
		case <-l.stopDrain:
			return
		}
	}
}

// publish sends an agent event to subscribers, registering permission
// requests so a client can answer them.
func (l *live) publish(e agent.Event, bg bool) Event {
	ev := Event{Session: l.id, Event: e}
	if e.Kind == agent.EvPermission {
		l.pmu.Lock()
		l.pseq++
		ev.RequestID = fmt.Sprintf("perm-%d", l.pseq)
		p := &pendingPerm{reply: e.Reply, bg: bg}
		l.pending[ev.RequestID] = p
		// Publish while holding pmu so the stored copy carries the seq.
		p.ev = l.bus.publish(ev)
		l.pmu.Unlock()
		return p.ev
	}
	return l.bus.publish(ev)
}

func (l *live) status(busy bool) {
	l.bus.publish(Event{Session: l.id, Busy: &busy, Event: agent.Event{Kind: EvStatus}})
}

// prompt starts a turn. The caller gets events from the bus.
func (l *live) prompt(text string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("session closed")
	}
	if l.busy {
		return errBusy
	}
	ctx := l.startLocked()
	l.bus.publish(Event{Session: l.id, Event: agent.Event{Kind: EvUserMessage, Text: text}})
	go l.loop(ctx, l.a.Run(ctx, text))
	return nil
}

// deliverNotifications runs a notification turn if the session is idle
// and finished background results are waiting.
func (l *live) deliverNotifications() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.busy || l.closed || l.a.PendingNotifications() == 0 {
		return
	}
	ctx := l.startLocked()
	go l.loop(ctx, nil)
}

// startLocked marks the session busy; l.mu must be held.
func (l *live) startLocked() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	l.busy, l.cancel = true, cancel
	l.runs.Add(1)
	l.status(true)
	return ctx
}

// loop drains a turn, then keeps delivering background results that
// arrived meanwhile, and finally marks the session idle.
func (l *live) loop(ctx context.Context, ch <-chan agent.Event) {
	defer l.runs.Done()
	for {
		if ch == nil && ctx.Err() == nil {
			ch, _ = l.a.RunNotifications(ctx)
		}
		if ch != nil {
			for e := range ch {
				l.publish(e, false)
			}
			ch = nil
			continue
		}
		l.mu.Lock()
		if ctx.Err() == nil && l.a.PendingNotifications() > 0 && !l.closed {
			l.mu.Unlock()
			continue // a task finished after RunNotifications looked
		}
		l.busy = false
		l.cancel()
		l.cancel = nil
		l.expire(false)
		l.status(false)
		l.mu.Unlock()
		return
	}
}

// interrupt cancels the current run, if any.
func (l *live) interrupt() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel == nil {
		return false
	}
	l.cancel()
	return true
}

// idleDo runs fn as a run of its own (busy, cancellable), failing if a
// turn is already running.
func (l *live) idleDo(parent context.Context, fn func(ctx context.Context) error) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return errors.New("session closed")
	}
	if l.busy {
		l.mu.Unlock()
		return errBusy
	}
	ctx, cancel := context.WithCancel(parent)
	l.busy, l.cancel = true, cancel
	l.runs.Add(1)
	l.status(true)
	l.mu.Unlock()

	err := fn(ctx)

	l.mu.Lock()
	l.busy, l.cancel = false, nil
	cancel()
	l.status(false)
	l.runs.Done()
	l.mu.Unlock()
	l.deliverNotifications()
	return err
}

func (l *live) isBusy() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.busy
}

// answer delivers a permission reply.
func (l *live) answer(id string, r agent.PermissionReply) bool {
	l.pmu.Lock()
	p, ok := l.pending[id]
	delete(l.pending, id)
	l.pmu.Unlock()
	if !ok {
		return false
	}
	p.reply <- r // buffered by the agent; never blocks
	verdict := "denied"
	if r.Allow {
		verdict = "allowed"
	}
	l.bus.publish(Event{Session: l.id, RequestID: id, Event: agent.Event{Kind: EvPermResolved, Text: verdict, ToolID: p.ev.ToolID}})
	return true
}

// expire drops requests nobody can answer any more: those of a finished
// run, or all of them when the background tasks are gone too.
func (l *live) expire(all bool) {
	l.pmu.Lock()
	var gone []string
	for id, p := range l.pending {
		if all || !p.bg {
			gone = append(gone, id)
			delete(l.pending, id)
		}
	}
	l.pmu.Unlock()
	sort.Strings(gone)
	for _, id := range gone {
		l.bus.publish(Event{Session: l.id, RequestID: id, Event: agent.Event{Kind: EvPermResolved, Text: "expired"}})
	}
}

func (l *live) pendingPerms() []Event {
	l.pmu.Lock()
	defer l.pmu.Unlock()
	out := make([]Event, 0, len(l.pending))
	for _, p := range l.pending {
		out = append(out, p.ev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// close stops the run and background work, ends the session and
// disconnects subscribers.
func (l *live) close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	if l.cancel != nil {
		l.cancel()
	}
	l.mu.Unlock()
	l.runs.Wait()
	l.a.StopAllBackground()
	close(l.stopDrain)
	<-l.drained
	l.expire(true)
	l.s.Close("other")
	l.bus.publish(Event{Session: l.id, Event: agent.Event{Kind: EvClosed}})
	l.bus.close()
}
