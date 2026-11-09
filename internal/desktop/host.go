package desktop

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/egoist/mygo/plugins/terminal"

	"larik/internal/session"
	"larik/internal/status"
)

// The host keeps the tabs' programs running when the window closes, as
// Rex and herdr keep their sessions: it runs each tab's program in a
// terminal no view shows, and the window's tabs show it through a
// connection, typing into it and showing the frames the host sends
// (terminal.Mirror), screen and scrollback cell for cell. A window
// opening again attaches to the tabs it had, mid-turn, rather than
// resuming their sessions from their transcripts.
//
// One host runs per data directory, on a socket beside the window's,
// started by the first window that needs it. A connection starts with a
// request, a line of JSON (hostRequest), and the host's answer, another
// (hostReply). A connection that opened or attached a tab then carries
// messages both ways, a type byte, a length and what it says: the
// window's input, size and colours; the host's frames, the size they're
// drawn at, the program's bells and notifications, and its exit.

// What a tab's connection carries.
const (
	// From the host.
	msgSize   = 'S' // the size the frames that follow are drawn at: cols, rows
	msgFrame  = 'F' // what brings the window's terminal to what the host's shows
	msgBell   = 'B'
	msgNotify = 'N' // a notification: [title, body], as JSON
	msgStatus = 'P' // a program status report, as JSON
	msgAgent  = 'A' // native coding-agent identity, as JSON
	msgExit   = 'X' // the program ended, with this code; nothing follows
	// From the window.
	msgInput  = 'I' // what was typed
	msgResize = 'R' // the size the view fits: cols, rows, and a cell's in pixels
	msgTheme  = 'T' // the colours the view shows, as JSON
)

// maxMessage bounds a message: a frame that starts a window over, with
// its scrollback, is the largest.
const maxMessage = 256 << 20

// hostFrameEvery is how often the host frames a tab for each window
// showing it, when it changed.
const hostFrameEvery = 30 * time.Millisecond

// hostIdle is how long the host waits, with no tab and no window, before
// it exits; hostKeepExited how long it keeps a tab whose program ended,
// for a window to come and see how, while none shows it.
const (
	hostIdle       = 10 * time.Second
	hostKeepExited = 10 * time.Minute
)

// hostRequest is what a connection asks the host first.
type hostRequest struct {
	// Op is "open", to run a program in a new tab, or attach to the tab
	// with ID when the host has it (running it again when it ended);
	// "attach", to attach to it only; "list"; "read", for the text its
	// terminal shows, scrollback and all; "send", to type Text into it,
	// and Return after with Enter; "close", to end its program and forget
	// it; "detach", to keep a terminal without a pane; "stop", to end every program and exit; "index", for the
	// sessions of its data folder, as a window on another machine shows
	// them (remote.go); or "ping".
	Op      string   `json:"op"`
	ID      string   `json:"id,omitempty"`
	Command []string `json:"command,omitempty"`
	Dir     string   `json:"dir,omitempty"`
	Env     []string `json:"env,omitempty"`
	Cols    int      `json:"cols,omitempty"`
	Rows    int      `json:"rows,omitempty"`
	// Shell and Run say what the tab is to the window, a terminal tab and
	// the command it was given, for a window that opens to it not knowing.
	Shell   bool                  `json:"shell,omitempty"`
	Run     string                `json:"run,omitempty"`
	Text    string                `json:"text,omitempty"`
	Enter   bool                  `json:"enter,omitempty"`
	Status  string                `json:"status,omitempty"` // an OSC 7501 payload from a hook
	Agent   *agentResume          `json:"agent,omitempty"`
	File    *remoteFileRequest    `json:"file,omitempty"`
	Project *remoteProjectRequest `json:"project,omitempty"`
}

// hostReply is the host's answer.
type hostReply struct {
	Error string        `json:"error,omitempty"`
	Tabs  []hostTabInfo `json:"tabs,omitempty"`
	Text  string        `json:"text,omitempty"` // read's
	PID   int           `json:"pid,omitempty"`  // ping's: the host's process
	// Proto is ping's: the protocol the host speaks, hostProto.
	Proto int `json:"proto,omitempty"`
	// Features are optional so a newer window can keep using an older
	// host's existing operations without assuming it has newer ones.
	Features     []string `json:"features,omitempty"`
	WorktreeRoot string   `json:"worktree_root,omitempty"`
	// Projects are index's: the sessions of the host's data folder.
	Projects []session.Project   `json:"projects,omitempty"`
	File     *remoteFileReply    `json:"file,omitempty"`
	Project  *remoteProjectReply `json:"project,omitempty"`
	Meta     *remoteMetaReply    `json:"meta,omitempty"`
}

// hostProto is the protocol the host speaks: 2 added index, for windows
// on other machines. A window needs a host that speaks its own, or newer.
const hostProto = 2

// hostTabInfo is a tab the host keeps.
type hostTabInfo struct {
	ID       string        `json:"id"`
	Dir      string        `json:"dir"`
	Command  []string      `json:"command"`
	Shell    bool          `json:"shell,omitempty"`
	Run      string        `json:"run,omitempty"`
	Title    string        `json:"title,omitempty"` // what its program set as the terminal's title
	Status   status.Report `json:"status,omitzero"`
	Agent    agentResume   `json:"agent,omitzero"`
	Exited   bool          `json:"exited,omitempty"`
	Code     int           `json:"code,omitempty"`
	Windows  int           `json:"windows,omitempty"` // connections showing it
	Detached bool          `json:"detached,omitempty"`
	Started  time.Time     `json:"started"`
}

// hostSocketPath is the host's socket for dataDir, in the temp folder as
// the window's is.
func hostSocketPath(dataDir string) string {
	sum := sha256.Sum256([]byte(dataDir))
	name := "larik-desktop-host-" + hex.EncodeToString(sum[:6]) + ".sock"
	return filepath.Join(shortSocketDir(name), name)
}

// host is the running host.
type host struct {
	dataDir string     // whose sessions index answers
	scratch bool       // and with those under the temp folder: tests'
	openMu  sync.Mutex // serializes opens and closes of a tab id
	closing bool       // guarded by openMu: no new tab after closeAll
	mu      sync.Mutex
	tabs    map[string]*hostTab
	conns   int       // connections open, of any kind
	idle    time.Time // since when it has had no tab and no connection
	// stop closes when a request asks the host to exit.
	stop     chan struct{}
	stopOnce sync.Once
}

// hostTab is a tab's program and the windows showing it.
type hostTab struct {
	id       string
	dir      string
	command  []string
	shell    bool
	run      string
	term     *terminal.Terminal
	clients  map[*hostClient]bool
	detached bool
	exited   bool
	code     int
	started  time.Time
	ended    time.Time
	records  statusRecords
	agent    agentResume
}

// hostClient is a window showing a tab: what's to be sent to it.
type hostClient struct {
	send chan hostMsg
}

type hostMsg struct {
	kind byte
	data []byte
}

// RunHost runs the host for dataDir until it has had no tab and no
// window for a while, or is asked to stop, or told to (SIGTERM), when it
// ends its programs. It returns at once, with no error, when another
// host has the socket. Its pid is in a file beside the socket.
func RunHost(dataDir string) error {
	if err := Prepare(); err != nil {
		return err
	}
	path := hostSocketPath(dataDir)
	unlock, err := lockSocket(path)
	if err != nil {
		return err
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	if pingHost(dataDir) == nil {
		return nil
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	ln, err = ownSocket(ln, path)
	if err != nil {
		return err
	}
	defer ln.Close()
	pid := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(path+".pid", []byte(pid), 0o600); err == nil {
		defer func() {
			unlock, err := lockSocket(path)
			if err != nil {
				return
			}
			defer unlock()
			if current, err := os.ReadFile(path + ".pid"); err == nil && string(current) == pid {
				_ = os.Remove(path + ".pid")
			}
		}()
	}
	log.Printf("host: started for %s, pid %d", dataDir, os.Getpid())
	h := newHost()
	h.dataDir = dataDir
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(signals)
	go func() {
		select {
		case <-h.waitIdle():
			log.Print("host: nothing to run, exiting")
		case <-h.stop:
			log.Print("host: asked to stop")
		case sig := <-signals:
			log.Printf("host: %v", sig)
		}
		ln.Close()
	}()
	served := make(chan struct{})
	ready := make(chan struct{})
	go func() {
		close(ready)
		h.serve(ln)
		close(served)
	}()
	<-ready
	unlock()
	unlock = nil
	<-served
	h.closeAll()
	return nil
}

func newHost() *host {
	return &host{tabs: map[string]*hostTab{}, idle: time.Now(), stop: make(chan struct{})}
}

// serve takes connections until the listener closes.
func (h *host) serve(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go h.handle(conn)
	}
}

// waitIdle closes the channel it returns once the host has had nothing
// to do for hostIdle, forgetting, as it waits, tabs whose programs ended
// long ago.
func (h *host) waitIdle() <-chan struct{} {
	idle := make(chan struct{})
	go func() {
		h.idleLoop()
		close(idle)
	}()
	return idle
}

func (h *host) idleLoop() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
		case <-h.stop:
			return
		}
		h.mu.Lock()
		h.expireTabs(time.Now())
		busy := len(h.tabs) > 0 || h.conns > 0
		if busy {
			h.idle = time.Now()
		}
		done := !busy && time.Since(h.idle) > hostIdle
		h.mu.Unlock()
		if done {
			return
		}
	}
}

// expireTabs runs with h.mu held. A detached final screen stays until the
// user ends it or the host itself stops.
func (h *host) expireTabs(now time.Time) {
	for id, t := range h.tabs {
		if t.exited && !t.detached && len(t.clients) == 0 && now.Sub(t.ended) > hostKeepExited {
			delete(h.tabs, id)
		}
	}
}

// closeAll ends every tab's program, as the host exits. A terminal
// closing calls OnExit, which takes h.mu: they close after.
func (h *host) closeAll() {
	h.openMu.Lock()
	h.closing = true
	h.mu.Lock()
	tabs := h.tabs
	h.tabs = map[string]*hostTab{}
	for _, t := range tabs {
		if !t.exited {
			log.Printf("host: ending tab %s", t.id)
		}
	}
	h.mu.Unlock()
	h.openMu.Unlock()
	for _, t := range tabs {
		t.term.Close()
	}
}

func (h *host) handle(conn net.Conn) {
	h.mu.Lock()
	h.conns++
	h.mu.Unlock()
	defer func() {
		conn.Close()
		h.mu.Lock()
		h.conns--
		h.mu.Unlock()
	}()
	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return
	}
	var req hostRequest
	reply := func(rep hostReply) bool {
		data, _ := json.Marshal(rep)
		_, err := conn.Write(append(data, '\n'))
		return err == nil
	}
	if err := json.Unmarshal(line, &req); err != nil {
		reply(hostReply{Error: "bad request: " + err.Error()})
		return
	}
	switch req.Op {
	case "ping":
		reply(hostReply{PID: os.Getpid(), Proto: hostProto, Features: []string{"session-index", "program-status", "remote-files", "remote-git", "remote-worktrees", "remote-meta", "agent-check", "detached-terminals"}, WorktreeRoot: filepath.Join(h.dataDir, "worktrees")})
	case "meta":
		meta, err := h.remoteMeta()
		if err != nil {
			reply(hostReply{Error: err.Error()})
			return
		}
		reply(hostReply{Meta: &meta})
	case "agent-check":
		if req.Agent == nil || !req.Agent.valid() {
			reply(hostReply{Error: "invalid agent identity"})
			return
		}
		name := req.Agent.Kind
		if _, err := exec.LookPath(name); err != nil {
			reply(hostReply{Error: name + " is not installed on this server"})
			return
		}
		reply(hostReply{})
	case "file":
		if req.File == nil {
			reply(hostReply{Error: "missing file request"})
			return
		}
		result, err := h.fileRequest(*req.File)
		if err != nil {
			reply(hostReply{Error: err.Error()})
			return
		}
		reply(hostReply{File: &result})
	case "project":
		if req.Project == nil {
			reply(hostReply{Error: "missing project request"})
			return
		}
		result, err := h.projectRequest(*req.Project)
		if err != nil {
			reply(hostReply{Error: err.Error()})
			return
		}
		reply(hostReply{Project: &result})
	case "index":
		projects, err := session.Index(h.dataDir, session.IndexOptions{Scratch: h.scratch})
		if err != nil {
			reply(hostReply{Error: err.Error()})
			return
		}
		reply(hostReply{Projects: projects})
	case "list":
		reply(hostReply{Tabs: h.list()})
	case "read", "send":
		h.mu.Lock()
		t := h.tabs[req.ID]
		exited := t != nil && t.exited
		h.mu.Unlock()
		switch {
		case t == nil:
			reply(hostReply{Error: fmt.Sprintf("no tab %s", req.ID)})
		case req.Op == "read":
			reply(hostReply{Text: t.term.Text()})
		case exited:
			reply(hostReply{Error: "the tab's program has exited"})
		default:
			if strings.ContainsAny(req.Text, "\r\n") {
				t.term.Paste(req.Text) // as one, not a line at a time
			} else {
				t.term.Send([]byte(req.Text))
			}
			if req.Enter {
				t.term.Send([]byte("\r"))
			}
			reply(hostReply{})
		}
	case "status":
		h.mu.Lock()
		t := h.tabs[req.ID]
		valid := t != nil && t.shell && !t.exited
		h.mu.Unlock()
		if !valid {
			reply(hostReply{Error: "status needs a running terminal tab"})
			return
		}
		if _, _, _, ok := parseProgramStatus(req.Status); !ok {
			reply(hostReply{Error: "invalid status report"})
			return
		}
		if req.Agent != nil && req.Agent.valid() && req.Agent.Tab == t.id {
			h.mu.Lock()
			t.agent = *req.Agent
			h.mu.Unlock()
			data, _ := json.Marshal(req.Agent)
			h.broadcast(t, hostMsg{kind: msgAgent, data: data})
		}
		h.statusEvent(t, func(s *statusRecords) (status.Report, bool) { return s.apply(req.Status) })
		reply(hostReply{})
	case "close":
		h.closeTab(req.ID)
		reply(hostReply{})
	case "detach":
		h.mu.Lock()
		t := h.tabs[req.ID]
		if t != nil && t.shell {
			t.detached = true
		}
		h.mu.Unlock()
		if t == nil || !t.shell {
			reply(hostReply{Error: "detach needs a terminal tab"})
			return
		}
		reply(hostReply{})
	case "stop":
		reply(hostReply{})
		h.stopOnce.Do(func() { close(h.stop) })
	case "open", "attach":
		t, err := h.tab(req)
		if err != nil {
			reply(hostReply{Error: err.Error()})
			return
		}
		if !reply(hostReply{}) {
			return
		}
		h.attach(t, conn, r, req.Op == "attach")
	default:
		reply(hostReply{Error: "unknown op " + req.Op})
	}
}

func (h *host) list() []hostTabInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]hostTabInfo, 0, len(h.tabs))
	for _, t := range h.tabs {
		out = append(out, hostTabInfo{ID: t.id, Dir: t.dir, Command: t.command, Shell: t.shell, Run: t.run, Title: t.term.Title(), Status: t.records.current(), Agent: t.agent, Exited: t.exited, Code: t.code, Windows: len(t.clients), Detached: t.detached, Started: t.started})
	}
	return out
}

// closeTab ends the tab's program and forgets the tab.
func (h *host) closeTab(id string) {
	h.openMu.Lock()
	defer h.openMu.Unlock()
	h.mu.Lock()
	t := h.tabs[id]
	delete(h.tabs, id)
	h.mu.Unlock()
	if t != nil {
		log.Printf("host: closing tab %s", id)
		t.term.Close()
		<-t.term.Done()
	}
}

// tab is the tab a request opens or attaches to: the host's, or for
// "open", a new one running the request's program, as when the one
// before it ended.
func (h *host) tab(req hostRequest) (*hostTab, error) {
	if req.ID == "" {
		return nil, errors.New("no tab id")
	}
	h.openMu.Lock()
	defer h.openMu.Unlock()
	if h.closing {
		return nil, errors.New("the host is stopping")
	}
	h.mu.Lock()
	t := h.tabs[req.ID]
	exited := t != nil && t.exited
	h.mu.Unlock()
	switch {
	case t != nil && (!exited || req.Op == "attach"):
		return t, nil
	case req.Op == "attach":
		return nil, fmt.Errorf("no tab %s", req.ID)
	}
	t = &hostTab{id: req.ID, dir: req.Dir, command: req.Command, shell: req.Shell, run: req.Run, started: time.Now(), clients: map[*hostClient]bool{}}
	term, err := terminal.New(terminal.Options{
		Command: req.Command,
		Dir:     req.Dir,
		Env:     req.Env,
		OnBell:  func() { h.broadcast(t, hostMsg{kind: msgBell}) },
		OnNotify: func(title, body string) {
			data, _ := json.Marshal([2]string{title, body})
			h.broadcast(t, hostMsg{kind: msgNotify, data: data})
		},
		OnStatus: func(payload string) {
			if !t.shell {
				return
			}
			h.statusEvent(t, func(s *statusRecords) (status.Report, bool) { return s.apply(payload) })
		},
		OnPrompt: func() {
			if t.shell {
				h.statusEvent(t, (*statusRecords).dropLive)
			}
		},
		OnReset: func() {
			if t.shell {
				h.statusEvent(t, (*statusRecords).clearAll)
			}
		},
		OnExit: func(code int) {
			h.mu.Lock()
			report, changed := t.records.dropLive()
			t.exited, t.code, t.ended = true, code, time.Now()
			h.mu.Unlock()
			if changed {
				data, _ := json.Marshal(report)
				h.broadcast(t, hostMsg{kind: msgStatus, data: data})
			}
			log.Printf("host: tab %s exited with %d", t.id, code)
		},
	})
	if err != nil {
		return nil, err
	}
	if req.Cols > 0 && req.Rows > 0 {
		term.Resize(req.Cols, req.Rows)
	}
	log.Printf("host: tab %s runs %q in %s", req.ID, req.Command, req.Dir)
	t.term = term
	h.mu.Lock()
	old := h.tabs[req.ID]
	h.tabs[req.ID] = t
	h.mu.Unlock()
	if old != nil {
		old.term.Close() // ended already: what's left of it
	}
	return t, nil
}

func (h *host) statusEvent(t *hostTab, update func(*statusRecords) (status.Report, bool)) {
	h.mu.Lock()
	report, changed := update(&t.records)
	h.mu.Unlock()
	if changed {
		data, _ := json.Marshal(report)
		h.broadcast(t, hostMsg{kind: msgStatus, data: data})
	}
}

// broadcast sends m to every window showing t, dropping it for one too
// far behind.
func (h *host) broadcast(t *hostTab, m hostMsg) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range t.clients {
		select {
		case c.send <- m:
		default:
		}
	}
}

// attach shows t to the window on conn until either goes: what the
// window sends goes to the program, and frames of what it shows come
// back, starting with all of it.
func (h *host) attach(t *hostTab, conn net.Conn, r *bufio.Reader, reattach bool) {
	m, err := t.term.NewMirror()
	if err != nil {
		return
	}
	defer m.Close()
	c := &hostClient{send: make(chan hostMsg, 64)}
	h.mu.Lock()
	t.clients[c] = true
	// An initial open can still be establishing its mirror when the user
	// detaches its pane. Only an explicit attach may clear that marker.
	if reattach {
		t.detached = false
	}
	current := t.records.current()
	if current.State != status.Idle {
		data, _ := json.Marshal(current)
		c.send <- hostMsg{kind: msgStatus, data: data}
	}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(t.clients, c)
		h.mu.Unlock()
	}()

	gone := make(chan struct{})
	go func() {
		defer close(gone)
		for {
			kind, data, err := readMsg(r)
			if err != nil {
				return
			}
			switch kind {
			case msgInput:
				t.term.Send(data)
			case msgResize:
				if len(data) == 16 {
					t.term.ResizePixels(int(binary.BigEndian.Uint32(data)), int(binary.BigEndian.Uint32(data[4:])),
						int(binary.BigEndian.Uint32(data[8:])), int(binary.BigEndian.Uint32(data[12:])))
				}
			case msgTheme:
				var th terminal.Theme
				if json.Unmarshal(data, &th) == nil {
					t.term.UseTheme(&th)
				}
			}
		}
	}()

	tick := time.NewTicker(hostFrameEvery)
	defer tick.Stop()
	var cols, rows int
	frame := func() error {
		fr, _ := m.Frame()
		if len(fr) == 0 {
			return nil
		}
		if c, r := t.term.Size(); c != cols || r != rows {
			cols, rows = c, r
			size := make([]byte, 8)
			binary.BigEndian.PutUint32(size, uint32(c))
			binary.BigEndian.PutUint32(size[4:], uint32(r))
			if err := writeMsg(conn, msgSize, size); err != nil {
				return err
			}
		}
		return writeMsg(conn, msgFrame, fr)
	}
	for {
		select {
		case <-gone:
			return
		case msg := <-c.send:
			if err := writeMsg(conn, msg.kind, msg.data); err != nil {
				return
			}
		case <-t.term.Done():
			// The last of what it printed, then how it ended.
			if frame() != nil {
				return
			}
			b := make([]byte, 4)
			binary.BigEndian.PutUint32(b, uint32(int32(t.term.ExitCode())))
			_ = writeMsg(conn, msgExit, b)
			return
		case <-tick.C:
			if err := frame(); err != nil {
				log.Printf("host: tab %s: %v", t.id, err)
				return
			}
		}
	}
}

// writeMsg writes a message: its type, its length, and it.
func writeMsg(w io.Writer, kind byte, data []byte) error {
	head := make([]byte, 5, 5+len(data))
	head[0] = kind
	binary.BigEndian.PutUint32(head[1:], uint32(len(data)))
	_, err := w.Write(append(head, data...))
	return err
}

// readMsg reads a message writeMsg wrote.
func readMsg(r io.Reader) (byte, []byte, error) {
	head := make([]byte, 5)
	if _, err := io.ReadFull(r, head); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(head[1:])
	if n > maxMessage {
		return 0, nil, fmt.Errorf("a message of %d bytes", n)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return 0, nil, err
	}
	return head[0], data, nil
}

// openHosted connects t to the host, when the window's tabs run there,
// starting the host when it isn't running: to run args in a new tab of
// the host's, or with t.reattach, to t's program running there already.
// It's nil when the tabs run in the window.
func (a *App) openHosted(t *tab, args []string) (*hostConn, error) {
	if name, dir, ok := splitRemote(t.dir); ok {
		return a.openRemote(t, name, dir, args)
	}
	if !a.opts.Host {
		return nil, nil
	}
	req := hostRequest{Op: "open", ID: t.id, Command: args, Dir: t.dir, Env: append([]string{status.TabEnv + "=" + t.id}, a.databaseStore.maskedEnv()...), Shell: t.shell(), Run: t.run}
	if t.reattach {
		req.Op = "attach"
	}
	if err := a.ensureHost(); err != nil {
		return nil, err
	}
	conn, r, _, err := askHost(a.opts.DataDir, req)
	if err != nil {
		return nil, err
	}
	return newHostConn(conn, r), nil
}

// ensureHost starts the host, unless it's running, and waits for it.
func (a *App) ensureHost() error {
	_, err := EnsureHost(a.opts.DataDir, a.opts.StartHost)
	return err
}

// EnsureHost starts the host for dataDir with start, unless it's running,
// waits for it, and returns its socket's path: larik desktop host ensure,
// which a window on another machine runs over SSH, to reach it there.
func EnsureHost(dataDir string, start func() error) (string, error) {
	path := hostSocketPath(dataDir)
	if pingHost(dataDir) == nil {
		return path, nil
	}
	if start == nil {
		return "", errors.New("no way to start it")
	}
	if err := start(); err != nil {
		return "", err
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		err := pingHost(dataDir)
		if err == nil {
			return path, nil
		}
		if time.Now().After(deadline) {
			return "", err
		}
	}
}

// hostedTabs are the tabs the host runs the programs of, by id, when the
// window's tabs run there and the host is running: those a window opening
// attaches to again.
func (a *App) hostedTabs() map[string]hostTabInfo {
	if !a.opts.Host {
		return nil
	}
	tabs, err := hostTabs(a.opts.DataDir)
	if err != nil {
		return nil
	}
	out := map[string]hostTabInfo{}
	for _, t := range tabs {
		if !t.Exited {
			out[t.ID] = t
		}
	}
	return out
}

// askHost sends the host for dataDir a request, and returns its answer
// and the connection, for a tab's messages after; the caller closes it.
func askHost(dataDir string, req hostRequest) (net.Conn, *bufio.Reader, hostReply, error) {
	return askHostVia(localHost(dataDir), req)
}

// dialer reaches a host: this machine's, on its socket, or a server's,
// through SSH (remote.go).
type dialer func() (net.Conn, error)

// localHost dials the host for dataDir on this machine.
func localHost(dataDir string) dialer {
	return func() (net.Conn, error) { return net.DialTimeout("unix", hostSocketPath(dataDir), time.Second) }
}

// askHostVia sends the host dial reaches a request, as askHost does.
func askHostVia(dial dialer, req hostRequest) (net.Conn, *bufio.Reader, hostReply, error) {
	conn, err := dial()
	if err != nil {
		return nil, nil, hostReply{}, err
	}
	data, _ := json.Marshal(req)
	deadline := 5 * time.Second
	if req.File != nil || req.Op == "meta" {
		deadline = 15 * time.Second
	}
	if req.Project != nil {
		deadline = actionTimeout + 5*time.Second
	}
	_ = conn.SetDeadline(time.Now().Add(deadline))
	if _, err := conn.Write(append(data, '\n')); err != nil {
		conn.Close()
		return nil, nil, hostReply{}, err
	}
	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil {
		conn.Close()
		return nil, nil, hostReply{}, err
	}
	_ = conn.SetDeadline(time.Time{})
	var rep hostReply
	if err := json.Unmarshal(line, &rep); err != nil {
		conn.Close()
		return nil, nil, hostReply{}, err
	}
	if rep.Error != "" {
		conn.Close()
		return nil, nil, rep, errors.New(rep.Error)
	}
	return conn, r, rep, nil
}

// pingHost reports whether the host for dataDir answers.
func pingHost(dataDir string) error {
	conn, _, _, err := askHost(dataDir, hostRequest{Op: "ping"})
	if err == nil {
		conn.Close()
	}
	return err
}

// hostTabs lists the tabs the host for dataDir keeps.
func hostTabs(dataDir string) ([]hostTabInfo, error) {
	conn, _, rep, err := askHost(dataDir, hostRequest{Op: "list"})
	if err != nil {
		return nil, err
	}
	conn.Close()
	return rep.Tabs, nil
}

// closeHostTab ends a tab's program in the host and has it forget it.
func closeHostTab(dataDir, id string) error {
	conn, _, _, err := askHost(dataDir, hostRequest{Op: "close", ID: id})
	if err == nil {
		conn.Close()
	}
	return err
}

// hostConn is a window's tab's connection to the host, the Conn of the
// tab's terminal: what's typed goes to the host's, and the frames the
// host sends are what the terminal reads. The host decides the size the
// frames are drawn at, from the size the view asked for, so the terminal
// takes it from here (Options.ResizeByConn).
type hostConn struct {
	conn net.Conn
	r    *bufio.Reader
	// term is the terminal reading, once it is made; ready closes then.
	term  *terminal.Terminal
	ready chan struct{}
	once  sync.Once
	// onBell and onNotify are the program's bells and notifications.
	onBell   func()
	onNotify func(title, body string)
	onStatus func(status.Report)
	onAgent  func(agentResume)
	// pending is what's left of the frame Read last read; code is how the
	// program ended, -1 until the host says, as when the host went.
	pending []byte
	wmu     sync.Mutex
	code    atomic.Int32
}

func newHostConn(conn net.Conn, r *bufio.Reader) *hostConn {
	c := &hostConn{conn: conn, r: r, ready: make(chan struct{})}
	c.code.Store(-1)
	return c
}

// attached gives the connection its terminal, for the sizes the host
// sends.
func (c *hostConn) attached(term *terminal.Terminal) {
	c.term = term
	c.once.Do(func() { close(c.ready) })
}

func (c *hostConn) Read(p []byte) (int, error) {
	for len(c.pending) == 0 {
		kind, data, err := readMsg(c.r)
		if err != nil {
			return 0, io.EOF
		}
		switch kind {
		case msgFrame:
			c.pending = data
		case msgSize:
			if len(data) == 8 {
				<-c.ready
				// What came before is drawn at the old size, and what
				// follows at this one.
				c.term.Resize(int(binary.BigEndian.Uint32(data)), int(binary.BigEndian.Uint32(data[4:])))
			}
		case msgBell:
			if c.onBell != nil {
				c.onBell()
			}
		case msgNotify:
			var n [2]string
			if json.Unmarshal(data, &n) == nil && c.onNotify != nil {
				c.onNotify(n[0], n[1])
			}
		case msgStatus:
			var report status.Report
			if json.Unmarshal(data, &report) == nil && c.onStatus != nil {
				c.onStatus(report)
			}
		case msgAgent:
			var agent agentResume
			if json.Unmarshal(data, &agent) == nil && c.onAgent != nil {
				c.onAgent(agent)
			}
		case msgExit:
			if len(data) == 4 {
				c.code.Store(int32(binary.BigEndian.Uint32(data)))
			}
			return 0, io.EOF
		}
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func (c *hostConn) send(kind byte, data []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return writeMsg(c.conn, kind, data)
}

func (c *hostConn) Write(p []byte) (int, error) {
	if err := c.send(msgInput, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// ResizePixels asks the host for the size the view fits.
func (c *hostConn) ResizePixels(cols, rows, cellWidth, cellHeight int) error {
	b := make([]byte, 16)
	for i, v := range []int{cols, rows, cellWidth, cellHeight} {
		binary.BigEndian.PutUint32(b[4*i:], uint32(v))
	}
	return c.send(msgResize, b)
}

// UseTheme gives the host's terminal the view's colours, for programs
// that ask what they are.
func (c *hostConn) UseTheme(th *terminal.Theme) error {
	data, err := json.Marshal(th)
	if err != nil {
		return err
	}
	return c.send(msgTheme, data)
}

// Close leaves the tab: its program goes on in the host.
func (c *hostConn) Close() error { return c.conn.Close() }

// exitCode is how the program ended, once Read said it did: -1 when the
// host went instead.
func (c *hostConn) exitCode() int { return int(c.code.Load()) }

// attachTab adds a tab showing the program the host runs as h: a
// session's larik, at path when the window knows it, or a terminal tab's.
func (a *App) attachTab(h hostTabInfo, path string) *tab {
	t := &tab{id: h.ID, dir: h.Dir, path: path, reattach: true}
	t.agent = agentForTab(t, h.Agent)
	if h.Shell {
		t.path, t.run, t.cmd = "", h.Run, shellCommand(h.Run)
	}
	if !a.run(t, "") {
		return nil
	}
	a.tabs = append(a.tabs, t)
	return t
}

// adoptHosted attaches to the tabs the host runs that the window has no
// tab for, as those of a window that ended before it saved them, in the
// order the host started them, and returns them.
func (a *App) adoptHosted(live map[string]hostTabInfo) []*tab {
	if a.skipAdoption {
		return nil
	}
	var left []hostTabInfo
	for id, h := range live {
		if !h.Detached && a.tabByID(id) == nil && !a.ownedHostTabs[id] {
			left = append(left, h)
		}
	}
	slices.SortFunc(left, func(x, y hostTabInfo) int { return x.Started.Compare(y.Started) })
	var out []*tab
	for _, h := range left {
		if t := a.attachTab(h, ""); t != nil {
			out = append(out, t)
		}
	}
	a.followSessions()
	return out
}

// quitEndingCommand is Quit and End Tabs, when the host runs the tabs'
// programs, which quitting otherwise leaves running; else none.
func (a *App) quitEndingCommand() []command {
	if !a.opts.Host {
		return nil
	}
	ending := a.quitEnding
	if a.quitAll != nil {
		ending = a.quitAll
	}
	return []command{{label: "Quit and End Panes", key: "Alt+Cmd+Q", run: ending}}
}
