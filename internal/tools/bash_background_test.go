package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

type bgResult struct {
	label, result string
	failed        bool
}

// fakeStarter runs background work at once, reporting its result on a
// channel; stop cancels it as the agent's task_stop would.
func fakeStarter() (BackgroundStarter, <-chan bgResult, context.CancelFunc) {
	results := make(chan bgResult, 1)
	ctx, stop := context.WithCancel(context.Background())
	start := func(label string, fn func(context.Context) (string, bool)) (string, error) {
		go func() {
			r, failed := fn(ctx)
			results <- bgResult{label, r, failed}
		}()
		return "bg-1", nil
	}
	return start, results, stop
}

func TestBashBackground(t *testing.T) {
	env := NewEnv(t.TempDir())
	env.RawOutputDir = t.TempDir()
	start, results, stop := fakeStarter()
	defer stop()
	ctx := WithCallID(WithBackground(context.Background(), start), "call1")
	r := Bash{}.Run(ctx, env, json.RawMessage(`{"command":"echo started; sleep 0.2; echo finished; exit 3","run_in_background":true}`))
	if r.IsError || !strings.Contains(r.Content, "Started bg-1") || !strings.Contains(r.Content, "raw_output tool_call_id=call1") {
		t.Fatalf("got %+v", r)
	}
	select {
	case got := <-results:
		if !got.failed || !strings.Contains(got.result, "finished") || !strings.Contains(got.result, "exit code 3") {
			t.Fatalf("result %+v", got)
		}
		if got.label != "bash: echo started; sleep 0.2; echo finished; exit 3" {
			t.Errorf("label %q", got.label)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the background command never reported")
	}
	entries, _ := os.ReadDir(env.RawOutputDir)
	if len(entries) != 1 {
		t.Fatalf("the output should be kept for raw_output: %v", entries)
	}
}

func TestBashBackgroundStop(t *testing.T) {
	env := NewEnv(t.TempDir())
	start, results, stop := fakeStarter()
	ctx := WithBackground(context.Background(), start)
	if r := (Bash{}).Run(ctx, env, json.RawMessage(`{"command":"sleep 30","run_in_background":true}`)); r.IsError {
		t.Fatalf("got %+v", r)
	}
	began := time.Now()
	stop()
	select {
	case got := <-results:
		if !got.failed || !strings.Contains(got.result, "stopped") || time.Since(began) > 5*time.Second {
			t.Fatalf("result %+v after %s", got, time.Since(began))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stopping should kill the command")
	}
}

func TestBashBackgroundUnavailable(t *testing.T) {
	env := NewEnv(t.TempDir())
	in := json.RawMessage(`{"command":"true","run_in_background":true}`)
	if r := (Bash{}).Run(context.Background(), env, in); !r.IsError || !strings.Contains(r.Content, "isn't available here") {
		t.Fatalf("without a starter: %+v", r)
	}
}
