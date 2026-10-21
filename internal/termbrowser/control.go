package termbrowser

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/chromedp/cdproto/target"
)

// The viewer (larik browser view, in a split pane) and this session share
// one Chrome. The session tells the viewer which tab to show over a Unix
// socket only the user can open: one JSON State per line, the current one
// on connect and another whenever it changes. The viewer sends back the
// user's answers to what only they can answer (a site's password prompt),
// one ViewerMessage per line. It talks to Chrome itself for the pixels and
// input, so frames never pass through here.

// State is what the viewer needs: where Chrome is and which tab to follow.
type State struct {
	// WS is the browser's DevTools endpoint; empty while Chrome is down.
	WS string `json:"ws"`
	// Focus is the tab an agent last used, which the viewer shows.
	Focus string `json:"focus,omitempty"`
	// Driving is the label of the agent acting on Focus right now, or "".
	Driving string `json:"driving,omitempty"`
	// Sound says pages may play audio (browser.sound).
	Sound bool       `json:"sound,omitempty"`
	Tabs  []StateTab `json:"tabs,omitempty"`
	// Auth is a site waiting for the user's username and password.
	Auth *AuthPrompt `json:"auth,omitempty"`
	// Location is what "Allow" shares when a page asks where the user is
	// (browser.location); nil when none is set.
	Location *Location `json:"location,omitempty"`
}

// Location is a position to report to pages, in degrees and meters.
type Location struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Accuracy  float64 `json:"accuracy,omitempty"`
}

// ViewerMessage is something the viewer sends the session.
type ViewerMessage struct {
	Op string `json:"op"` // "auth"
	ID int    `json:"id,omitempty"`
	// For "auth": the credentials, or Cancel. Never logged or stored.
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
	Cancel   bool   `json:"cancel,omitempty"`
}

// StateTab is one tab the session knows, and the agent it belongs to.
type StateTab struct {
	ID    string `json:"id"`
	Owner string `json:"owner,omitempty"`
}

// drivingFor is how long after an action the viewer keeps saying an
// agent is driving.
const drivingFor = 3 * time.Second

// viewState is the session's side of State. It has its own lock: it
// changes while s.mu is held, and is read by socket writers that must not
// wait for a browser action.
type viewState struct {
	mu      sync.Mutex
	focus   target.ID
	owner   string
	until   time.Time                  // driving until
	subs    map[chan struct{}]net.Conn // each viewer's wake-up, and its connection
	ln      net.Listener
	path    string
	stopped bool

	auths    []*pendingAuth // sign-ins waiting for the user, oldest first
	nextAuth int
}

// focusOn records that owner is using t. The caller holds s.mu.
func (s *Session) focusOn(t *tab) {
	v := &s.view
	v.mu.Lock()
	moved := v.focus != t.id || v.owner != t.owner
	idle := time.Now().After(v.until)
	v.focus, v.owner, v.until = t.id, t.owner, time.Now().Add(drivingFor)
	v.mu.Unlock()
	if moved || idle {
		s.changed()
		// Say when the agent has stopped, so the viewer drops its badge.
		time.AfterFunc(drivingFor+100*time.Millisecond, s.changed)
	}
}

// changed wakes every viewer to send it the current state.
func (s *Session) changed() {
	v := &s.view
	v.mu.Lock()
	defer v.mu.Unlock()
	for ch := range v.subs {
		select {
		case ch <- struct{}{}:
		default: // one is already pending
		}
	}
}

// state snapshots State. It takes s.mu briefly, so never call it with s.mu
// held.
func (s *Session) state() State {
	s.mu.Lock()
	st := State{WS: s.wsURL, Sound: s.opts.Sound, Location: s.opts.Location}
	for _, t := range s.tabs {
		st.Tabs = append(st.Tabs, StateTab{ID: string(t.id), Owner: t.owner})
	}
	if t := s.active[""]; t != nil {
		st.Focus = string(t.id)
	}
	s.mu.Unlock()
	v := &s.view
	v.mu.Lock()
	if v.focus != "" {
		st.Focus = string(v.focus)
	}
	if time.Now().Before(v.until) {
		st.Driving = firstNonEmpty(v.owner, "agent")
	}
	if len(v.auths) > 0 {
		a := v.auths[0].prompt
		st.Auth = &a
	}
	v.mu.Unlock()
	return st
}

// Serve starts the control socket, unless it is already up, and returns
// its path. dir is where it goes; a path too long for a Unix socket
// falls back to the temporary directory.
func (s *Session) Serve(dir string) (string, error) {
	v := &s.view
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.ln != nil {
		return v.path, nil
	}
	if v.stopped {
		return "", fmt.Errorf("the browser session is closed")
	}
	path, err := socketPath(dir, fmt.Sprintf("larik-browser-%d.sock", os.Getpid()))
	if err != nil {
		return "", err
	}
	_ = os.Remove(path) // left by a crashed process with our pid
	ln, err := net.Listen("unix", path)
	if err != nil {
		return "", err
	}
	_ = os.Chmod(path, 0o600)
	v.ln, v.path, v.subs = ln, path, map[chan struct{}]net.Conn{}
	go s.accept(ln)
	return path, nil
}

// socketPath puts name in dir (made private to the user), or in a private
// directory under the system's temporary one when that path is too long.
func socketPath(dir, name string) (string, error) {
	const maxSocketPath = 100 // sun_path is 104 bytes on macOS, 108 on Linux
	if dir != "" {
		if p := filepath.Join(dir, name); len(p) <= maxSocketPath {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return "", err
			}
			return p, nil
		}
	}
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("larik-%d", os.Getuid()))
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(tmp, name), nil
}

func (s *Session) accept(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go s.stream(conn)
	}
}

// stream sends conn the state now and on every change until it hangs up.
func (s *Session) stream(conn net.Conn) {
	defer conn.Close()
	if !sameUser(conn) {
		return
	}
	wake := make(chan struct{}, 1)
	v := &s.view
	v.mu.Lock()
	if v.subs == nil || v.stopped {
		v.mu.Unlock()
		return
	}
	v.subs[wake] = conn
	v.mu.Unlock()
	defer func() {
		v.mu.Lock()
		delete(v.subs, wake)
		last := len(v.subs) == 0 && !v.stopped
		v.mu.Unlock()
		if last {
			s.paneClosed()
		}
	}()
	// Reading takes the viewer's messages, and tells us when it goes away.
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		dec := json.NewDecoder(bufio.NewReader(conn))
		for {
			var m ViewerMessage
			if dec.Decode(&m) != nil {
				return
			}
			if m.Op == "auth" {
				_ = s.answerAuth(m)
			}
		}
	}()
	enc := json.NewEncoder(conn)
	last := ""
	for {
		st := s.state()
		if b, _ := json.Marshal(st); string(b) != last {
			last = string(b)
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if enc.Encode(st) != nil {
				return
			}
		}
		select {
		case <-wake:
		case <-gone:
			return
		}
	}
}

// paneClosed shuts Chrome down when the last viewer has gone: closing the
// browser pane means being done with the browser. The agent's next browser
// call starts it again, with sign-ins kept in the profile.
func (s *Session) paneClosed() {
	s.mu.Lock()
	running := s.root != nil
	if running {
		s.shutdown()
	}
	s.mu.Unlock()
	if running {
		s.note("The user closed the browser pane, which also closed the browser: its tabs are gone. The next browser call starts it again; sign-ins are kept.")
	}
}

// stopServing closes the control socket and every viewer's connection.
func (s *Session) stopServing() {
	v := &s.view
	v.mu.Lock()
	defer v.mu.Unlock()
	v.stopped = true
	if v.ln != nil {
		v.ln.Close()
		os.Remove(v.path)
		v.ln = nil
	}
	for _, conn := range v.subs {
		conn.Close() // the viewer sees the session end and quits
	}
}

// Control is a viewer's connection to a session.
type Control struct {
	conn net.Conn
	mu   sync.Mutex // guards writes
}

// DialControl connects to a session's control socket.
func DialControl(path string) (*Control, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	return &Control{conn: conn}, nil
}

// Run calls fn with each State the session sends, until the connection
// ends.
func (c *Control) Run(fn func(State)) error {
	dec := json.NewDecoder(bufio.NewReader(c.conn))
	for {
		var st State
		if err := dec.Decode(&st); err != nil {
			return err
		}
		fn(st)
	}
}

// Send sends the session a message.
func (c *Control) Send(m ViewerMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = c.conn.Write(append(b, '\n'))
	return err
}

func (c *Control) Close() error { return c.conn.Close() }

// Subscribe connects to a session's control socket and calls fn with
// each State it sends, until the connection ends.
func Subscribe(path string, fn func(State)) error {
	c, err := DialControl(path)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Run(fn)
}

// viewers is how many viewers are connected now.
func (s *Session) viewers() int {
	v := &s.view
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.subs)
}
