package termbrowser

import (
	"context"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/chromedp/cdproto/target"
)

// Everything the user does to the page goes to Chrome through one queue,
// one command at a time, so a click can't overtake the move before it and
// keys arrive in the order they were typed. While a command is in flight,
// pointer moves collapse into the latest position and wheel ticks add up,
// so a fast trackpad swipe is a few smooth scrolls instead of a burst of
// overlapping ones.

const (
	// wheelStep is how far one wheel event scrolls, in CSS pixels: about
	// a line. Terminals send one event per line of a trackpad swipe, so a
	// mouse-wheel notch's 100-plus would make swipes overshoot.
	wheelStep = 40
	// gestureGap ends a scroll gesture: events further apart start a new
	// one.
	gestureGap = 250 * time.Millisecond
)

type inputKind int

const (
	inputOther inputKind = iota
	inputMove
	inputWheel
)

type inputItem struct {
	kind    inputKind
	session target.SessionID
	method  string
	params  any // a map[string]any for wheel items, which merge
}

// inputQueue sends the user's input to the shown tab in order.
type inputQueue struct {
	mu    sync.Mutex
	conn  *cdpConn
	items []inputItem
	kick  chan struct{}
	done  chan struct{}
	note  func(string) // reports a failure to the pane

	// The scroll gesture under way: which axis it moves along, and when
	// its last event came.
	axis     int // 0 none yet, 1 vertical, 2 horizontal
	lastTick time.Time
	sideways int // sideways ticks seen in a row, before a gesture locks
}

func newInputQueue(note func(string)) *inputQueue {
	q := &inputQueue{kick: make(chan struct{}, 1), done: make(chan struct{}), note: note}
	go q.run()
	return q
}

// use points the queue at a connection, dropping input meant for another.
func (q *inputQueue) use(conn *cdpConn) {
	q.mu.Lock()
	if q.conn != conn {
		q.conn, q.items = conn, nil
	}
	q.mu.Unlock()
}

func (q *inputQueue) push(it inputItem) {
	q.mu.Lock()
	if n := len(q.items); n > 0 {
		last := &q.items[n-1]
		switch {
		case it.kind == inputMove && last.kind == inputMove && last.session == it.session:
			*last = it // only where the pointer is now matters
			q.mu.Unlock()
			return
		case it.kind == inputWheel && last.kind == inputWheel && last.session == it.session:
			a, b := last.params.(map[string]any), it.params.(map[string]any)
			a["deltaX"] = a["deltaX"].(float64) + b["deltaX"].(float64)
			a["deltaY"] = a["deltaY"].(float64) + b["deltaY"].(float64)
			a["x"], a["y"] = b["x"], b["y"]
			q.mu.Unlock()
			return
		}
	}
	q.items = append(q.items, it)
	q.mu.Unlock()
	select {
	case q.kick <- struct{}{}:
	default:
	}
}

func (q *inputQueue) run() {
	for {
		select {
		case <-q.kick:
		case <-q.done:
			return
		}
		for {
			q.mu.Lock()
			if len(q.items) == 0 {
				q.mu.Unlock()
				break
			}
			it, conn := q.items[0], q.conn
			q.items = q.items[1:]
			q.mu.Unlock()
			if conn == nil {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := conn.call(ctx, it.session, it.method, it.params, nil)
			cancel()
			if err != nil && q.note != nil {
				q.note(err.Error())
			}
		}
	}
}

func (q *inputQueue) close() {
	select {
	case <-q.done:
	default:
		close(q.done)
	}
}

// wheelDelta turns one wheel event into a scroll, or reports false for a
// sideways tick that is a trackpad's wobble during a vertical swipe: it
// only scrolls once a gesture has gone sideways on purpose. Shift turns
// the wheel sideways, as in desktop browsers.
func (q *inputQueue) wheelDelta(b tea.MouseButton, shift bool, now time.Time) (dx, dy float64, ok bool) {
	switch b {
	case tea.MouseWheelUp:
		dy = -wheelStep
	case tea.MouseWheelDown:
		dy = wheelStep
	case tea.MouseWheelLeft:
		dx = -wheelStep
	case tea.MouseWheelRight:
		dx = wheelStep
	default:
		return 0, 0, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if now.Sub(q.lastTick) > gestureGap {
		q.axis, q.sideways = 0, 0
	}
	q.lastTick = now
	if shift && dy != 0 {
		// Shift+wheel is sideways on purpose: no wobble to filter.
		q.axis = 2
		return dy, 0, true
	}
	vertical := dy != 0
	switch q.axis {
	case 1:
		return 0, dy, vertical
	case 2:
		return dx, 0, !vertical
	}
	if vertical {
		q.axis, q.sideways = 1, 0
		return 0, dy, true
	}
	// Two sideways ticks before any vertical one: a sideways swipe.
	if q.sideways++; q.sideways >= 2 {
		q.axis = 2
		return dx * 2, 0, true // the first tick, held back, counts too
	}
	return 0, 0, false
}
