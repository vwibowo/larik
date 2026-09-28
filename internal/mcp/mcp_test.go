package mcp

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"larik/internal/config"
)

// buildServer compiles testdata/server once per test binary.
func buildServer(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "mcp-test-server")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/server")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build test server: %v\n%s", err, out)
	}
	return bin
}

func newCfg(t *testing.T, servers map[string]config.MCPServer) *config.Config {
	dir := t.TempDir()
	for name, s := range servers {
		s.Name = name
		servers[name] = s
	}
	return &config.Config{Cwd: dir, DataDir: filepath.Join(dir, "data"), MCPServers: servers, ApprovedMCP: map[string]string{}}
}

func call(t *testing.T, m *Manager, name, input string) (string, bool) {
	t.Helper()
	for _, tl := range m.Tools(context.Background()) {
		if tl.Spec().Name == name {
			r := tl.Run(context.Background(), nil, json.RawMessage(input))
			return r.Content, r.IsError
		}
	}
	t.Fatalf("tool %s not found", name)
	return "", false
}

func TestStdioServer(t *testing.T) {
	bin := buildServer(t)
	t.Setenv("LARIK_TEST_ARG", "expanded")
	cfg := newCfg(t, map[string]config.MCPServer{
		"local": {Command: bin, Args: []string{"${LARIK_TEST_ARG}"}, Env: map[string]string{"GREETING": "hi "}, Trusted: true},
	})
	m := NewManager(cfg, "test")
	m.Start()
	defer m.Close()

	reg := m.Registry(context.Background(), func(s string) { t.Log("notice:", s) })
	var names []string
	for _, s := range reg.Specs() {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, ","); got != "read,write,edit,bash,raw_output,grep,glob,mcp__local__echo,mcp__local__fail" {
		t.Fatalf("tools = %s", got)
	}

	echo, _ := reg.Get("mcp__local__echo")
	if !echo.ReadOnly() {
		t.Error("read-only closed-world tool should be ReadOnly")
	}
	if fail, _ := reg.Get("mcp__local__fail"); fail.ReadOnly() {
		t.Error("tool without annotations must not be ReadOnly")
	}
	if !strings.Contains(string(echo.Spec().Schema), `"text"`) || !strings.Contains(echo.Spec().Description, `MCP server "local"`) {
		t.Errorf("spec = %+v", echo.Spec())
	}

	if out, isErr := call(t, m, "mcp__local__echo", `{"text":"there"}`); out != "hi there" || isErr {
		t.Errorf("echo = %q err=%v", out, isErr)
	}
	if out, isErr := call(t, m, "mcp__local__fail", `{}`); out != "boom" || !isErr {
		t.Errorf("fail = %q err=%v", out, isErr)
	}

	// Server stderr goes to the log, not the terminal.
	st := m.Statuses()[0]
	log, _ := os.ReadFile(st.LogPath)
	if !strings.Contains(string(log), "greeting=hi") || !strings.Contains(string(log), "args=expanded") {
		t.Errorf("log = %q", log)
	}
}

// serveTest starts the test server with flag ("-http" or "-sse") on a
// free port and returns its address once it listens.
func serveTest(t *testing.T, bin, flag string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	srv := exec.Command(bin, flag, addr)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Process.Kill() })
	for i := 0; i < 50; i++ { // wait for listen
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return addr
}

// TestSSEServerStaysConnected checks that the SSE transport's stream
// outlives the connect timeout's context: tool calls made after
// connecting must still reach the server.
func TestSSEServerStaysConnected(t *testing.T) {
	addr := serveTest(t, buildServer(t), "-sse")
	cfg := newCfg(t, map[string]config.MCPServer{
		"old": {Type: "sse", URL: "http://" + addr + "/", Trusted: true},
	})
	m := NewManager(cfg, "test")
	m.Start()
	defer m.Close()
	m.Registry(context.Background(), func(s string) { t.Log("notice:", s) })
	for i := range 2 {
		if out, isErr := call(t, m, "mcp__old__echo", `{"text":"over sse"}`); isErr || out != "over sse" {
			t.Fatalf("call %d: echo = %q (error %v)", i, out, isErr)
		}
	}
}

func TestHTTPServerWithHeaders(t *testing.T) {
	bin := buildServer(t)
	addr := serveTest(t, bin, "-http")

	// Put an auth proxy in front that requires the expanded header.
	t.Setenv("LARIK_TEST_TOKEN", "s3cret")
	proxy := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer s3cret" {
			http.Error(w, "unauthorized", 401)
			return
		}
		r.URL.Scheme, r.URL.Host, r.RequestURI = "http", addr, ""
		resp, err := http.DefaultTransport.RoundTrip(r)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				w.Write(buf[:n])
				w.(http.Flusher).Flush()
			}
			if err != nil {
				return
			}
		}
	})}
	pl, _ := net.Listen("tcp", "127.0.0.1:0")
	go proxy.Serve(pl)
	defer proxy.Close()

	cfg := newCfg(t, map[string]config.MCPServer{
		"remote": {Type: "http", URL: "http://" + pl.Addr().String() + "/mcp", Headers: map[string]string{"Authorization": "Bearer ${LARIK_TEST_TOKEN}"}, Trusted: true},
		"nokey":  {Type: "http", URL: "http://" + pl.Addr().String() + "/mcp", Trusted: true},
	})
	m := NewManager(cfg, "test")
	m.Start()
	defer m.Close()
	var notices []string
	m.Registry(context.Background(), func(s string) { notices = append(notices, s) })

	if out, _ := call(t, m, "mcp__remote__echo", `{"text":"over http"}`); out != "over http" {
		t.Errorf("echo = %q", out)
	}
	if joined := strings.Join(notices, "\n"); !strings.Contains(joined, `"nokey" failed`) || !strings.Contains(joined, "Unauthorized") {
		t.Errorf("expected failure notice for nokey, got %v", notices)
	}
}

func TestProjectServersNeedApproval(t *testing.T) {
	bin := buildServer(t)
	cfg := newCfg(t, map[string]config.MCPServer{
		"proj": {Command: bin, Source: ".mcp.json"}, // untrusted
	})
	m := NewManager(cfg, "test")
	m.Start()
	defer m.Close()

	var notices []string
	reg := m.Registry(context.Background(), func(s string) { notices = append(notices, s) })
	if _, ok := reg.Get("mcp__proj__echo"); ok {
		t.Fatal("unapproved server must not start")
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "/mcp approve proj") {
		t.Fatalf("notices = %v", notices)
	}

	if err := m.Approve("proj"); err != nil {
		t.Fatal(err)
	}
	reg = m.Registry(context.Background(), func(string) {})
	if _, ok := reg.Get("mcp__proj__echo"); !ok {
		t.Fatal("approved server should start")
	}

	// Approval is persisted and pinned to the config hash.
	reloaded, err := config.Load(cfg.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	srv := cfg.MCPServers["proj"]
	if !reloaded.Approved(srv) {
		t.Error("approval not persisted")
	}
	srv.Args = []string{"--evil"}
	if reloaded.Approved(srv) {
		t.Error("changed config must need re-approval")
	}
}

func TestToolNames(t *testing.T) {
	if got := ToolName("my server", "get.issue"); got != "mcp__my_server__get_issue" {
		t.Errorf("got %s", got)
	}
	long := ToolName("server", strings.Repeat("x", 100))
	if len(long) != maxToolName || !strings.HasPrefix(long, "mcp__server__xxx") {
		t.Errorf("long name %q (%d)", long, len(long))
	}
	if ServerOf("mcp__gh__create_issue") != "gh" || ServerOf("bash") != "" {
		t.Error("ServerOf")
	}
}

func TestCollidingServerNamesKeepToolNamesUnique(t *testing.T) {
	bin := buildServer(t)
	cfg := newCfg(t, map[string]config.MCPServer{
		"a.b": {Command: bin, Trusted: true},
		"a_b": {Command: bin, Trusted: true},
	})
	m := NewManager(cfg, "test")
	m.Start()
	defer m.Close()
	var notices []string
	reg := m.Registry(context.Background(), func(s string) { notices = append(notices, s) })
	seen := map[string]bool{}
	for _, s := range reg.Specs() {
		if seen[s.Name] {
			t.Fatalf("tool name %s appears twice", s.Name)
		}
		seen[s.Name] = true
	}
	if !strings.Contains(strings.Join(notices, "\n"), "skipped") {
		t.Errorf("expected a notice about the skipped tools, got %v", notices)
	}
}
