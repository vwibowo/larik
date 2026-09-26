package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// conn is a minimal JSON-RPC 2.0 connection with LSP's Content-Length framing.
type conn struct {
	w   io.WriteCloser
	r   *bufio.Reader
	wmu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan message
	closed  bool
	err     error
	done    chan struct{}

	// Handlers run on their own goroutines so the read loop never blocks.
	onNotify  func(method string, params json.RawMessage)
	onRequest func(method string, params json.RawMessage) (any, error)
}

type message struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("lsp error %d: %s", e.Code, e.Message) }

var errClosed = errors.New("language server connection closed")

func newConn(r io.Reader, w io.WriteCloser) *conn {
	c := &conn{w: w, r: bufio.NewReaderSize(r, 64*1024), pending: map[int64]chan message{}, done: make(chan struct{})}
	return c
}

func (c *conn) start() { go c.readLoop() }

func (c *conn) write(m message) error {
	m.JSONRPC = "2.0"
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := fmt.Fprintf(c.w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = c.w.Write(body)
	return err
}

func (c *conn) call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errClosed
	}
	c.nextID++
	id := c.nextID
	ch := make(chan message, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	raw, _ := json.Marshal(id)
	rid := json.RawMessage(raw)
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if err := c.write(message{ID: &rid, Method: method, Params: p}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		_ = c.notify("$/cancelRequest", map[string]any{"id": id})
		return ctx.Err()
	case <-c.done:
		return errClosed
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && len(resp.Result) > 0 && string(resp.Result) != "null" {
			return json.Unmarshal(resp.Result, result)
		}
		return nil
	}
}

func (c *conn) notify(method string, params any) error {
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.write(message{Method: method, Params: p})
}

func (c *conn) readLoop() {
	defer c.shutdown(errClosed)
	for {
		m, err := c.read()
		if err != nil {
			c.shutdown(err)
			return
		}
		switch {
		case m.Method != "" && m.ID != nil: // server -> client request
			go c.reply(*m.ID, m.Method, m.Params)
		case m.Method != "": // notification
			if c.onNotify != nil {
				c.onNotify(m.Method, m.Params)
			}
		case m.ID != nil: // response
			var id int64
			if json.Unmarshal(*m.ID, &id) != nil {
				continue
			}
			c.mu.Lock()
			ch := c.pending[id]
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
}

func (c *conn) reply(id json.RawMessage, method string, params json.RawMessage) {
	var result any
	var err error
	if c.onRequest != nil {
		result, err = c.onRequest(method, params)
	} else {
		err = &rpcError{Code: -32601, Message: "method not found: " + method}
	}
	resp := message{ID: &id}
	if err != nil {
		var re *rpcError
		if !errors.As(err, &re) {
			re = &rpcError{Code: -32603, Message: err.Error()}
		}
		resp.Error = re
	} else {
		b, _ := json.Marshal(result)
		resp.Result = b
	}
	_ = c.write(resp)
}

func (c *conn) read() (message, error) {
	length := -1
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return message{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			if length, err = strconv.Atoi(strings.TrimSpace(v)); err != nil {
				return message{}, fmt.Errorf("bad Content-Length %q", v)
			}
		}
	}
	if length < 0 {
		return message{}, errors.New("missing Content-Length")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(c.r, body); err != nil {
		return message{}, err
	}
	var m message
	if err := json.Unmarshal(body, &m); err != nil {
		return message{}, err
	}
	return m, nil
}

func (c *conn) shutdown(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed, c.err = true, err
	close(c.done)
}

func (c *conn) close() error {
	c.shutdown(errClosed)
	return c.w.Close()
}
