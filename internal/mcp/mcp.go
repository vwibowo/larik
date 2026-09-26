// Package mcp connects to Model Context Protocol servers and exposes their
// tools to the agent as ordinary tools named mcp__<server>__<tool>.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"larik/internal/config"
	"larik/internal/tools"
)

// ConnectTimeout bounds how long one server may take to start and list tools.
const ConnectTimeout = 30 * time.Second

type State string

const (
	StateConnecting    State = "connecting"
	StateConnected     State = "connected"
	StateFailed        State = "failed"
	StateNeedsApproval State = "needs approval"
	StateDisabled      State = "disabled"
)

// Status is a snapshot of one server for display.
type Status struct {
	Name      string
	Transport string
	Source    string
	State     State
	Err       string
	Tools     []string // model-facing tool names
	LogPath   string
}

type server struct {
	cfg     config.MCPServer
	state   State
	err     error
	session *sdk.ClientSession
	tools   []tools.Tool
	logPath string
	logFile *os.File
	done    chan struct{} // closed when the connection attempt finishes
}

// Manager owns server connections for one Larik process.
type Manager struct {
	cfg     *config.Config
	client  *sdk.Client
	logDir  string
	version string

	// Base is the non-MCP tool set that Registry extends (defaults to the
	// built-in tools).
	Base []tools.Tool

	// Dial builds the transport for a server; tests replace it.
	Dial func(ctx context.Context, s *server) (sdk.Transport, error)

	mu      sync.Mutex
	servers map[string]*server
}

func NewManager(cfg *config.Config, version string) *Manager {
	m := &Manager{
		cfg:     cfg,
		client:  sdk.NewClient(&sdk.Implementation{Name: "larik", Version: version}, nil),
		logDir:  filepath.Join(cfg.DataDir, "logs"),
		version: version,
		servers: map[string]*server{},
		Base:    tools.Builtin(),
	}
	m.Dial = m.dial
	return m
}

// Start begins connecting every enabled, approved server that hasn't been
// attempted yet. It returns immediately; use Tools to wait.
func (m *Manager) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, cfg := range m.cfg.MCPServers {
		if s, ok := m.servers[name]; ok && s.state != StateNeedsApproval {
			continue
		}
		s := &server{cfg: cfg, done: make(chan struct{})}
		m.servers[name] = s
		switch {
		case cfg.Disabled:
			s.state = StateDisabled
			close(s.done)
		case !m.cfg.Approved(cfg):
			s.state = StateNeedsApproval
			close(s.done)
		default:
			s.state = StateConnecting
			go m.connect(s)
		}
	}
}

// Approve records approval for a project-scoped server and starts it.
func (m *Manager) Approve(name string) error {
	cfg, ok := m.cfg.MCPServers[name]
	if !ok {
		return fmt.Errorf("no MCP server named %q", name)
	}
	if err := config.ApproveMCP(m.cfg.Cwd, cfg); err != nil {
		return err
	}
	m.cfg.ApprovedMCP[name] = cfg.Hash()
	m.Start()
	return nil
}

// Tools waits for pending connections (bounded by ctx) and returns the tools
// of every connected server, sorted by name for a stable prompt prefix.
func (m *Manager) Tools(ctx context.Context) []tools.Tool {
	m.mu.Lock()
	var pending []*server
	for _, s := range m.servers {
		pending = append(pending, s)
	}
	m.mu.Unlock()
	for _, s := range pending {
		select {
		case <-s.done:
		case <-ctx.Done():
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	var out []tools.Tool
	for _, s := range m.servers {
		if s.state == StateConnected {
			out = append(out, s.tools...)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Spec().Name < out[j].Spec().Name })
	return out
}

// Statuses returns a snapshot sorted by server name.
func (m *Manager) Statuses() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Status
	for name, s := range m.servers {
		st := Status{Name: name, Transport: s.cfg.Transport(), Source: s.cfg.Source, State: s.state, LogPath: s.logPath}
		if s.err != nil {
			st.Err = s.err.Error()
		}
		for _, t := range s.tools {
			st.Tools = append(st.Tools, t.Spec().Name)
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Close shuts down all sessions (stdio servers get their stdin closed and
// are terminated if they don't exit).
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.servers {
		if s.session != nil {
			_ = s.session.Close()
		}
		if s.logFile != nil {
			_ = s.logFile.Close()
		}
	}
}

func (m *Manager) connect(s *server) {
	defer close(s.done)
	ctx, cancel := context.WithTimeout(context.Background(), ConnectTimeout)
	defer cancel()

	session, ts, err := m.open(ctx, s)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		s.state, s.err = StateFailed, err
		if session != nil {
			_ = session.Close()
		}
		return
	}
	s.session, s.tools, s.state = session, ts, StateConnected
}

func (m *Manager) open(ctx context.Context, s *server) (*sdk.ClientSession, []tools.Tool, error) {
	transport, err := m.Dial(ctx, s)
	if err != nil {
		return nil, nil, err
	}
	session, err := m.client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("connect: %w", err)
	}
	var ts []tools.Tool
	if res := session.InitializeResult(); res != nil && res.Capabilities != nil && res.Capabilities.Tools == nil {
		return session, nil, nil // server offers no tools (e.g. resources only)
	}
	used := map[string]bool{}
	for t, err := range session.Tools(ctx, nil) {
		if err != nil {
			return session, nil, fmt.Errorf("list tools: %w", err)
		}
		ts = append(ts, newTool(s.cfg.Name, session, t, used))
	}
	return session, ts, nil
}

// dial builds the real transport for a server config.
func (m *Manager) dial(ctx context.Context, s *server) (sdk.Transport, error) {
	cfg := s.cfg
	switch cfg.Transport() {
	case "stdio":
		if cfg.Command == "" {
			return nil, errors.New("stdio server needs a command")
		}
		args := make([]string, len(cfg.Args))
		for i, a := range cfg.Args {
			args[i] = config.ExpandEnv(a)
		}
		cmd := exec.Command(config.ExpandEnv(cfg.Command), args...)
		cmd.Dir = m.cfg.Cwd
		cmd.Env = os.Environ()
		for k, v := range cfg.Env {
			cmd.Env = append(cmd.Env, k+"="+config.ExpandEnv(v))
		}
		// Server stderr would corrupt the TUI; keep it in a log file instead.
		if err := os.MkdirAll(m.logDir, 0o700); err == nil {
			s.logPath = filepath.Join(m.logDir, "mcp-"+sanitize(cfg.Name)+".log")
			if f, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err == nil {
				s.logFile, cmd.Stderr = f, f
			}
		}
		return &sdk.CommandTransport{Command: cmd}, nil

	case "http", "sse":
		if cfg.URL == "" {
			return nil, errors.New("remote server needs a url")
		}
		client := &http.Client{Transport: headerTransport{headers: expandMap(cfg.Headers), base: http.DefaultTransport}}
		url := config.ExpandEnv(cfg.URL)
		if cfg.Transport() == "sse" {
			return &sdk.SSEClientTransport{Endpoint: url, HTTPClient: client}, nil
		}
		return &sdk.StreamableClientTransport{Endpoint: url, HTTPClient: client}, nil
	}
	return nil, fmt.Errorf("unsupported MCP transport %q", cfg.Type)
}

func expandMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = config.ExpandEnv(v)
	}
	return out
}

// headerTransport adds static headers (e.g. Authorization) to every request.
type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
}

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if len(h.headers) > 0 {
		r = r.Clone(r.Context())
		for k, v := range h.headers {
			r.Header.Set(k, v)
		}
	}
	return h.base.RoundTrip(r)
}

// Registry waits for servers (up to ConnectTimeout) and returns the built-in
// tools plus every connected server's tools. Problems are reported via notify.
func (m *Manager) Registry(ctx context.Context, notify func(string)) *tools.Registry {
	if len(m.cfg.MCPServers) == 0 {
		return tools.NewRegistry(m.Base...)
	}
	m.Start() // picks up servers approved since the last load
	if n := m.pending(); n > 0 {
		notify(fmt.Sprintf("waiting for %d MCP server(s) to start…", n))
	}
	ctx, cancel := context.WithTimeout(ctx, ConnectTimeout+5*time.Second)
	defer cancel()
	mcpTools := m.Tools(ctx)
	for _, st := range m.Statuses() {
		switch st.State {
		case StateFailed:
			msg := fmt.Sprintf("MCP server %q failed: %s", st.Name, st.Err)
			if st.LogPath != "" {
				msg += " (log: " + st.LogPath + ")"
			}
			notify(msg)
		case StateConnecting:
			notify(fmt.Sprintf("MCP server %q is still starting; its tools will load after /clear", st.Name))
		case StateNeedsApproval:
			notify(fmt.Sprintf("MCP server %q from %s is not approved yet; review it and run /mcp approve %s", st.Name, filepath.Base(st.Source), st.Name))
		}
	}
	return tools.NewRegistry(append(append([]tools.Tool(nil), m.Base...), mcpTools...)...)
}

func (m *Manager) pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.servers {
		if s.state == StateConnecting {
			n++
		}
	}
	return n
}

// Config returns a server's configuration.
func (m *Manager) Config(name string) (config.MCPServer, bool) {
	c, ok := m.cfg.MCPServers[name]
	return c, ok
}
