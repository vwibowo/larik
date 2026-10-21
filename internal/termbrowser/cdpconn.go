package termbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// cdpConn is a bare DevTools connection for the viewer. The viewer must
// not use chromedp contexts on the agent's tabs: cancelling one closes its
// tab. A raw connection only ever detaches, so closing it leaves Chrome and
// every tab as they were.
type cdpConn struct {
	conn *chromedp.Conn

	mu      sync.Mutex // guards writes, next and pending
	next    int64
	pending map[int64]chan *cdproto.Message
	closed  error

	events chan *cdproto.Message
}

// dialCDP connects to a browser endpoint. Events arrive on Events until
// the connection ends, when the channel closes.
func dialCDP(ctx context.Context, ws string) (*cdpConn, error) {
	conn, err := chromedp.DialContext(ctx, ws)
	if err != nil {
		return nil, err
	}
	c := &cdpConn{conn: conn, pending: map[int64]chan *cdproto.Message{}, events: make(chan *cdproto.Message, 64)}
	go c.read()
	return c, nil
}

func (c *cdpConn) read() {
	defer close(c.events)
	for {
		msg := new(cdproto.Message)
		if err := c.conn.Read(context.Background(), msg); err != nil {
			c.mu.Lock()
			c.closed = err
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}
		if msg.ID == 0 {
			// The reader of Events must never wait on a call itself (it
			// would wait for this loop), so blocking here is safe.
			c.events <- msg
			continue
		}
		c.mu.Lock()
		ch := c.pending[msg.ID]
		delete(c.pending, msg.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- msg
		}
	}
}

// Events delivers the browser's events, in the order they arrive.
func (c *cdpConn) Events() <-chan *cdproto.Message { return c.events }

// call sends method with params to session ("" is the browser) and decodes
// the result into result, when it isn't nil.
func (c *cdpConn) call(ctx context.Context, session target.SessionID, method string, params, result any) error {
	var raw []byte
	if params != nil {
		var err error
		if raw, err = json.Marshal(params); err != nil {
			return err
		}
	}
	ch := make(chan *cdproto.Message, 1)
	c.mu.Lock()
	if c.closed != nil {
		c.mu.Unlock()
		return c.closed
	}
	c.next++
	id := c.next
	c.pending[id] = ch
	err := c.conn.Write(ctx, &cdproto.Message{ID: id, SessionID: session, Method: cdproto.MethodType(method), Params: raw})
	c.mu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}
	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	case msg, ok := <-ch:
		if !ok {
			return errors.New("browser connection closed")
		}
		if msg.Error != nil {
			return fmt.Errorf("%s: %s", method, msg.Error.Message)
		}
		if result != nil && len(msg.Result) > 0 {
			return json.Unmarshal(msg.Result, result)
		}
		return nil
	}
}

// attach opens a flat session on a tab.
func (c *cdpConn) attach(ctx context.Context, id string) (target.SessionID, error) {
	var res struct {
		SessionID target.SessionID `json:"sessionId"`
	}
	err := c.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": id, "flatten": true}, &res)
	return res.SessionID, err
}

// detach ends a session without closing its tab.
func (c *cdpConn) detach(ctx context.Context, session target.SessionID) {
	_ = c.call(ctx, "", "Target.detachFromTarget", map[string]any{"sessionId": session}, nil)
}

func (c *cdpConn) Close() error { return c.conn.Close() }
