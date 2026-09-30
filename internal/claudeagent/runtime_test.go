//go:build !windows

package claudeagent

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"larik/internal/llm"
)

func fakeClaude(t *testing.T, auth string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	help := strings.Join(requiredFlags, " ") + " --max-turns"
	script := `#!/bin/sh
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
	runtime := &Runtime{Executable: fakeClaude(t, `{"loggedIn":true,"authMethod":"oauth","apiProvider":"firstParty"}`), Environ: func() []string {
		return []string{"PATH=/usr/bin:/bin", "ANTHROPIC_API_KEY=child-must-not-have", "CLAUDE_CODE_USE_BEDROCK=1"}
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
	bridge, err := startBridge(ctx, []llm.ToolSpec{{Name: "read_file", Schema: []byte(`{"type":"object"}`)}}, out, &callMatcher{})
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
