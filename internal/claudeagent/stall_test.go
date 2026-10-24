//go:build !windows

package claudeagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"larik/internal/llm"
)

// Reuse the fake CLI's local help/auth contract, replacing only inference.
func stallCLI(t *testing.T, body string) string {
	t.Helper()
	path := fakeClaude(t, `{"loggedIn":true,"authMethod":"oauth","apiProvider":"firstParty"}`)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(string(data), `    if [ "$HANG" = "1" ]; then sleep 30; fi`, body+"\n    exit 0", 1)
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func stallRequest() llm.AgentRuntimeRequest {
	return llm.AgentRuntimeRequest{Model: "sonnet", Messages: []llm.Message{llm.UserText("task")}}
}

func collectStallEvents(t *testing.T, events <-chan llm.AgentRuntimeEvent) []llm.AgentRuntimeEvent {
	t.Helper()
	var result []llm.AgentRuntimeEvent
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return result
			}
			result = append(result, event)
		case <-timer.C:
			t.Fatal("runtime did not finish")
			return nil
		}
	}
}

func TestWatchdogWarningDoesNotBlockCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	activity := newRuntimeActivity(80 * time.Millisecond)
	stop := activity.watch(ctx, cancel, make(chan llm.AgentRuntimeEvent))
	defer stop()
	select {
	case <-ctx.Done():
		if !activity.timedOut() {
			t.Fatal("expected watchdog expiration")
		}
	case <-time.After(time.Second):
		t.Fatal("undelivered warning blocked cancellation")
	}
}

func TestWatchdogWarningCappedAtOneMinute(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	activity := newRuntimeActivity(5 * time.Minute)
	activity.last = time.Now().Add(-59 * time.Second)
	out := make(chan llm.AgentRuntimeEvent, 1)
	stop := activity.watch(ctx, cancel, out)
	defer stop()
	select {
	case event := <-out:
		t.Fatalf("early warning: %+v", event)
	case <-time.After(20 * time.Millisecond):
	}
	activity.mu.Lock()
	activity.last = time.Now().Add(-61 * time.Second)
	activity.mu.Unlock()
	activity.changed()
	select {
	case event := <-out:
		if event.Notice == "" || !strings.Contains(event.Notice, "1m1") {
			t.Fatalf("expected elapsed warning: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("no warning after one minute")
	}
}

func TestRuntimeSilentStallWarnsAndCleansUp(t *testing.T) {
	dir, launches := t.TempDir(), filepath.Join(t.TempDir(), "launches")
	runtime := &Runtime{Executable: stallCLI(t, `echo run >> "$LAUNCHES"; sleep 30`), BridgeDir: dir, StallTimeout: 250 * time.Millisecond,
		Environ: func() []string { return []string{"PATH=/usr/bin:/bin", "LAUNCHES=" + launches} }}
	request := stallRequest()
	request.Tools = []llm.ToolSpec{{Name: "read", Schema: []byte(`{"type":"object"}`)}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	events, err := runtime.Run(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	warned, failed := false, false
	for event := range events {
		if event.Notice != "" {
			if time.Since(start) < runtime.StallTimeout/2 {
				t.Fatal("warning emitted before half the timeout")
			}
			if failed {
				t.Fatal("warning after error")
			}
			warned = true
		}
		if event.Err != nil {
			if !warned || !strings.Contains(event.Err.Error(), "stalled") || !strings.Contains(event.Err.Error(), "250ms") {
				t.Fatalf("unexpected error: %v (warned=%v)", event.Err, warned)
			}
			failed = true
			if files, _ := filepath.Glob(filepath.Join(dir, "claude-mcp-*.json")); len(files) != 0 {
				t.Fatalf("timeout error arrived before bridge cleanup: %v", files)
			}
		}
	}
	if !failed || time.Since(start) > 3*time.Second {
		t.Fatalf("timeout failed or cleanup too slow: failed=%v elapsed=%s", failed, time.Since(start))
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "claude-mcp-*.json")); len(files) != 0 {
		t.Fatalf("bridge config leaked: %v", files)
	}
	data, err := os.ReadFile(launches)
	if err != nil || string(data) != "run\n" {
		t.Fatalf("turn was retried: %q, %v", data, err)
	}
}

func TestRuntimeStallResetsOnPartialStreamActivity(t *testing.T) {
	// Whitespace inside a single unfinished JSON line must count as activity.
	runtime := &Runtime{Executable: stallCLI(t, `printf '{'; for i in 1 2 3 4 5 6; do sleep 0.1; printf ' '; done; printf '"type":"result","subtype":"success"}\n'`), StallTimeout: 400 * time.Millisecond}
	start := time.Now()
	events, err := runtime.Run(context.Background(), stallRequest())
	if err != nil {
		t.Fatal(err)
	}
	done := false
	for _, event := range collectStallEvents(t, events) {
		if event.Err != nil || event.Notice != "" {
			t.Fatalf("activity did not reset watchdog: %+v", event)
		}
		done = done || event.Done
	}
	if !done || time.Since(start) < runtime.StallTimeout {
		t.Fatal("expected a successful turn longer than the timeout")
	}
}

func TestRuntimePartialTurnStallDoesNotRetry(t *testing.T) {
	runtime := &Runtime{Executable: stallCLI(t, `printf '%s\n' '{"type":"stream_event","event":{"delta":{"type":"text_delta","text":"partial"}}}'; sleep 30`), StallTimeout: 200 * time.Millisecond}
	events, err := runtime.Run(context.Background(), stallRequest())
	if err != nil {
		t.Fatal(err)
	}
	texts, failures := 0, 0
	for _, event := range collectStallEvents(t, events) {
		if event.Text == "partial" {
			texts++
		}
		if event.Err != nil {
			failures++
		}
		if event.Done {
			t.Fatal("stalled partial turn reported success")
		}
	}
	if texts != 1 || failures != 1 {
		t.Fatalf("texts=%d failures=%d", texts, failures)
	}
}

func TestRuntimeStallDisabledAndCancellation(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second, time.Second} {
		t.Run(timeout.String(), func(t *testing.T) {
			runtime := &Runtime{Executable: stallCLI(t, "sleep 30"), StallTimeout: timeout}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			events, err := runtime.Run(ctx, stallRequest())
			if err != nil {
				t.Fatal(err)
			}
			select {
			case event := <-events:
				t.Fatalf("unexpected event before cancellation: %+v", event)
			case <-time.After(200 * time.Millisecond):
			}
			cancel()
			for _, event := range collectStallEvents(t, events) {
				if event.Err != nil || event.Notice != "" {
					t.Fatalf("cancellation reported a stall: %+v", event)
				}
			}
		})
	}
}

func TestRuntimeStallPausesForAllParallelBridgeCalls(t *testing.T) {
	runtime := &Runtime{Executable: stallCLI(t, "sleep 30"), BridgeDir: t.TempDir(), StallTimeout: 500 * time.Millisecond}
	request := stallRequest()
	request.Tools = []llm.ToolSpec{{Name: "read", Schema: []byte(`{"type":"object"}`)}}
	request.Parallel = []string{"read"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := runtime.Run(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(runtime.BridgeDir, "claude-mcp-*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("bridge config: %v %v", files, err)
	}
	bridge := &bridgeServer{configPath: files[0]}
	replies := make(chan string, 2)
	for _, id := range []string{"1", "2"} {
		go func() {
			replies <- bridgeCallContext(ctx, t, bridge, `{"jsonrpc":"2.0","id":`+id+`,"method":"tools/call","params":{"name":"read","arguments":{}}}`)
		}()
	}
	var calls []*llm.AgentRuntimeToolRequest
	for len(calls) < 2 {
		select {
		case event := <-events:
			if event.Tool == nil {
				t.Fatalf("expected pending tool, got %+v", event)
			}
			calls = append(calls, event.Tool)
		case <-time.After(3 * time.Second):
			t.Fatal("parallel calls did not reach bridge")
		}
	}
	// No replies means the calls can still be waiting for permission.
	assertQuiet := func() {
		t.Helper()
		select {
		case event := <-events:
			t.Fatalf("watchdog ran with a tool pending: %+v", event)
		case <-time.After(650 * time.Millisecond):
		}
	}
	assertQuiet()
	calls[0].Result <- llm.Block{Content: "first"}
	<-replies
	assertQuiet() // The second call must keep the watchdog paused.
	resumed := time.Now()
	calls[1].Result <- llm.Block{Content: "second"}
	<-replies
	warned, failed := false, false
	for _, event := range collectStallEvents(t, events) {
		warned = warned || event.Notice != ""
		if event.Err != nil {
			failed = true
		}
	}
	if !warned || !failed || time.Since(resumed) < runtime.StallTimeout {
		t.Fatalf("watchdog did not resume fresh: warned=%v failed=%v elapsed=%s", warned, failed, time.Since(resumed))
	}
}
