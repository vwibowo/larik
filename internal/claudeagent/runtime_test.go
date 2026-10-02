//go:build !windows

package claudeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"larik/internal/llm"
)

func fakeClaude(t *testing.T, auth string) string {
	return fakeClaudeWith(t, auth, strings.Join(isolationFlags, " "))
}

// fakeClaudeWith is a fake CLI whose help also lists extra flags. Run
// records its arguments, one per line, in $ARGS_FILE when set.
func fakeClaudeWith(t *testing.T, auth, extra string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	help := strings.Join(requiredFlags, " ") + " --max-turns " + extra
	script := `#!/bin/sh
if [ -n "$ARGS_FILE" ]; then printf '%s\n' "$@" > "$ARGS_FILE"; fi
case "$1" in
  --help) printf '%s\n' '` + help + `' ;;
  --version) printf '%s\n' 'test-1.0' ;;
  auth) printf '%s\n' '` + auth + `' ;;
  *)
    if [ "$HANG" = "1" ]; then sleep 30; fi
    if [ -n "$ANTHROPIC_API_KEY" ] || [ -n "$CLAUDE_CODE_USE_BEDROCK" ]; then exit 9; fi
    IFS= read -r input
    printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}}'
    printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}'
    printf '%s\n' '{"type":"result","subtype":"success","usage":{"input_tokens":7,"output_tokens":2}}'
    ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckClaudeCLIStates(t *testing.T) {
	ctx := context.Background()
	ready := &Runtime{Executable: fakeClaude(t, `{"loggedIn":true,"authMethod":"oauth","apiProvider":"firstParty"}`)}
	if got := ready.Check(ctx); !got.Ready() || got.Version != "test-1.0" || !got.MaxTurns {
		t.Fatalf("ready status = %+v", got)
	}
	loggedOut := &Runtime{Executable: fakeClaude(t, `{"loggedIn":false,"authMethod":"none","apiProvider":"firstParty"}`)}
	if got := loggedOut.Check(ctx); got.State != string(StateLoggedOut) {
		t.Fatalf("logged-out status = %+v", got)
	}
	wrongAuth := &Runtime{Executable: fakeClaude(t, `{"loggedIn":true,"authMethod":"api_key","apiProvider":"firstParty"}`)}
	if got := wrongAuth.Check(ctx); got.State != string(StateWrongAuth) {
		t.Fatalf("wrong-auth status = %+v", got)
	}
	if ready.Check(ctx).Isolation != "--restricted" {
		t.Fatal("--restricted should be preferred when the CLI has it")
	}
	older := &Runtime{Executable: fakeClaudeWith(t, `{"loggedIn":true,"authMethod":"oauth","apiProvider":"firstParty"}`, "--safe-mode")}
	if got := older.Check(ctx); !got.Ready() || got.Isolation != "--safe-mode" {
		t.Fatalf("a CLI without --restricted should fall back to --safe-mode: %+v", got)
	}
	neither := &Runtime{Executable: fakeClaudeWith(t, `{"loggedIn":true,"authMethod":"oauth","apiProvider":"firstParty"}`, "")}
	if got := neither.Check(ctx); got.State != string(StateIncompatible) {
		t.Fatalf("a CLI with neither isolation flag is incompatible: %+v", got)
	}
	missing := &Runtime{LookupPath: func(string) (string, error) { return "", os.ErrNotExist }}
	if got := missing.Check(ctx); got.State != string(StateMissing) {
		t.Fatalf("missing status = %+v", got)
	}
	incompatiblePath := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(incompatiblePath, []byte("#!/bin/sh\nprintf 'old cli\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := (&Runtime{Executable: incompatiblePath}).Check(ctx); got.State != string(StateIncompatible) {
		t.Fatalf("incompatible status = %+v", got)
	}
}

func TestRuntimeStreamsTextAndUsage(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "parent-key")
	argsFile := filepath.Join(t.TempDir(), "args")
	runtime := &Runtime{Executable: fakeClaude(t, `{"loggedIn":true,"authMethod":"oauth","apiProvider":"firstParty"}`), Environ: func() []string {
		return []string{"PATH=/usr/bin:/bin", "ANTHROPIC_API_KEY=child-must-not-have", "CLAUDE_CODE_USE_BEDROCK=1", "ARGS_FILE=" + argsFile}
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, err := runtime.Run(ctx, llm.AgentRuntimeRequest{Model: "sonnet", System: "system prompt", Messages: []llm.Message{llm.UserText("task")}, MaxTurns: 3})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	var assistant *llm.Message
	var usage *llm.Usage
	for event := range events {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		text += event.Text
		if event.Assistant != nil {
			assistant = event.Assistant
		}
		if event.Usage != nil {
			usage = event.Usage
		}
	}
	if text != "hello" || assistant == nil || assistant.Text() != "hello" || usage == nil || usage.Input != 7 || usage.Output != 2 {
		t.Fatalf("text=%q assistant=%+v usage=%+v", text, assistant, usage)
	}
	// --safe-mode would also turn off the MCP server that serves Larik's tools.
	args, _ := os.ReadFile(argsFile)
	if lines := strings.Split(string(args), "\n"); !slices.Contains(lines, "--restricted") || slices.Contains(lines, "--safe-mode") {
		t.Fatalf("the child should run with --restricted, not --safe-mode: %q", args)
	}
}

func TestMalformedStreamAndTurnLimit(t *testing.T) {
	out := make(chan llm.AgentRuntimeEvent, 4)
	if err := parseOutput(context.Background(), strings.NewReader("not-json\n"), out, &callMatcher{}, 20); err == nil || !strings.Contains(err.Error(), "malformed Claude stream JSON") {
		t.Fatalf("malformed error = %v", err)
	}
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"working"}]}}` + "\n"
	if err := parseOutput(context.Background(), strings.NewReader(line+line), out, &callMatcher{}, 1); err == nil || !strings.Contains(err.Error(), "limit of 1 model iterations") {
		t.Fatalf("turn limit error = %v", err)
	}
	if err := parseOutput(context.Background(), strings.NewReader(strings.Repeat("x", maxOutputLine+1)+"\n"), out, &callMatcher{}, 1); err == nil {
		t.Fatal("oversized stream line was accepted")
	}
}

func TestBridgeIsLoopbackAuthenticatedPrivateAndRemoved(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan llm.AgentRuntimeEvent, 1)
	bridge, err := startBridge(ctx, []llm.ToolSpec{{Name: "read_file", Schema: []byte(`{"type":"object"}`)}}, nil, out, &callMatcher{})
	if err != nil {
		t.Fatal(err)
	}
	path := bridge.configPath
	if !strings.HasPrefix(bridge.listener.Addr().String(), "127.0.0.1:") {
		t.Fatalf("bridge address = %s", bridge.listener.Addr())
	}
	if mode, err := ConfigPermissions(path); err != nil || mode != 0o600 {
		t.Fatalf("config permissions = %v, %v", mode, err)
	}
	response, err := http.Get("http://" + bridge.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", response.StatusCode)
	}
	bridge.Close(context.Background())
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temporary config remains: %v", err)
	}
}

func TestRuntimeCancellationStopsChild(t *testing.T) {
	runtime := &Runtime{Executable: fakeClaude(t, `{"loggedIn":true,"authMethod":"oauth","apiProvider":"firstParty"}`), Environ: func() []string { return []string{"PATH=/usr/bin:/bin", "HANG=1"} }}
	ctx, cancel := context.WithCancel(context.Background())
	events, err := runtime.Run(ctx, llm.AgentRuntimeRequest{Model: "sonnet", Messages: []llm.Message{llm.UserText("task")}})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan struct{})
	go func() {
		for range events {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runtime did not stop after cancellation")
	}
}

// bridgeCall posts one JSON-RPC request to the bridge and returns the body.
func bridgeCall(t *testing.T, bridge *bridgeServer, body string) string {
	t.Helper()
	config, err := os.ReadFile(bridge.configPath)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(config, &parsed); err != nil {
		t.Fatal(err)
	}
	server := parsed.MCPServers["larik"]
	req, _ := http.NewRequest("POST", server.URL, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range server.Headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Error(err)
		return ""
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return string(data)
}

// TestBridgeServesCallsConcurrently: parallel-safe tools are marked
// read-only, which is what lets Claude Code issue them together, and the
// bridge passes concurrent calls on without waiting for each other.
func TestBridgeServesCallsConcurrently(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan llm.AgentRuntimeEvent, 4)
	specs := []llm.ToolSpec{{Name: "grep", Schema: []byte(`{"type":"object"}`)}, {Name: "write", Schema: []byte(`{"type":"object"}`)}}
	bridge, err := startBridge(ctx, specs, []string{"grep"}, out, &callMatcher{})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close(context.Background())

	list := bridgeCall(t, bridge, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	var tools struct {
		Result struct {
			Tools []struct {
				Name        string
				Annotations *struct {
					ReadOnlyHint bool `json:"readOnlyHint"`
				}
			}
		}
	}
	if err := json.Unmarshal([]byte(list), &tools); err != nil || len(tools.Result.Tools) != 2 {
		t.Fatalf("tools/list: %v %s", err, list)
	}
	for _, tl := range tools.Result.Tools {
		readOnly := tl.Annotations != nil && tl.Annotations.ReadOnlyHint
		if readOnly != (tl.Name == "grep") {
			t.Errorf("%s readOnlyHint = %v", tl.Name, readOnly)
		}
	}

	// Two calls in flight: both must reach the agent before either is answered.
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bridgeCall(t, bridge, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"grep","arguments":{"n":%d}}}`, i+2, i))
		}()
	}
	var pending []*llm.AgentRuntimeToolRequest
	for len(pending) < 2 {
		select {
		case ev := <-out:
			pending = append(pending, ev.Tool)
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of 2 concurrent calls reached the agent", len(pending))
		}
	}
	for _, p := range pending {
		p.Result <- llm.Block{Type: llm.BlockToolResult, Content: "ok"}
	}
	wg.Wait()
}
