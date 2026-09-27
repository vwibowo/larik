package server

import (
	"sync"

	"larik/internal/agent"
)

// Server-generated event types, alongside the agent's own.
const (
	EvStatus       agent.EventKind = "status"              // Busy set
	EvUserMessage  agent.EventKind = "user_message"        // Text is the prompt
	EvPermResolved agent.EventKind = "permission_resolved" // RequestID; Text allowed/denied/expired
	EvClosed       agent.EventKind = "session_closed"
)

// Event is what clients receive: an agent event plus routing fields.
type Event struct {
	Seq       int64  `json:"seq"`
	Session   string `json:"session"`
	RequestID string `json:"request_id,omitempty"` // permission requests
	Busy      *bool  `json:"busy,omitempty"`       // EvStatus
	agent.Event
}

const (
	ringSize = 4096 // events kept for replay after a reconnect
	subQueue = 1024 // per-subscriber buffer before it is dropped
)

// bus fans a session's events out to subscribers and keeps recent ones
// so a reconnecting client can catch up with Last-Event-ID.
type bus struct {
	mu     sync.Mutex
	seq    int64
	ring   []Event // ring[i%ringSize]
	subs   map[chan Event]struct{}
	closed bool
}

func newBus() *bus { return &bus{ring: make([]Event, ringSize), subs: map[chan Event]struct{}{}} }

// publish assigns the next sequence number and delivers e. A subscriber
// that can't keep up is disconnected; it can reconnect and replay.
func (b *bus) publish(e Event) Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return e
	}
	b.seq++
	e.Seq = b.seq
	b.ring[b.seq%ringSize] = e
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
			delete(b.subs, ch)
			close(ch)
		}
	}
	return e
}

// subscribe returns the retained events after seq `after` and a channel
// for new ones. The channel is closed when the bus closes or the
// subscriber falls behind. gap reports that some events were lost.
func (b *bus) subscribe(after int64) (replay []Event, ch chan Event, gap bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch = make(chan Event, subQueue)
	if b.closed {
		close(ch)
		return nil, ch, false
	}
	oldest := max(b.seq-ringSize+1, 1)
	if after < oldest-1 {
		gap, after = true, oldest-1
	}
	for s := after + 1; s <= b.seq; s++ {
		replay = append(replay, b.ring[s%ringSize])
	}
	b.subs[ch] = struct{}{}
	return replay, ch, gap
}

func (b *bus) unsubscribe(ch chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subs[ch]; ok {
		delete(b.subs, ch)
		close(ch)
	}
}

func (b *bus) last() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

func (b *bus) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for ch := range b.subs {
		close(ch)
	}
	b.subs = nil
}
