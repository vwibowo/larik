package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/tools"
)

type runtimeTool struct{ calls int }

func (t *runtimeTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: "runtime_test", Description: "test runtime dispatch", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*runtimeTool) ReadOnly() bool { return true }
func (t *runtimeTool) Run(context.Context, *tools.Env, json.RawMessage) tools.Result {
	t.calls++
	return tools.Result{Content: "tool result"}
}

type scriptedRuntime struct{}

func (scriptedRuntime) Run(ctx context.Context, req llm.AgentRuntimeRequest) (<-chan llm.AgentRuntimeEvent, error) {
	out := make(chan llm.AgentRuntimeEvent, 4)
	call := llm.Block{Type: llm.BlockToolUse, ID: "runtime-call", Name: "runtime_test", Input: json.RawMessage(`{}`)}
	go func() {
		defer close(out)
		assistant := llm.Message{Role: llm.RoleAssistant, Model: req.Model, Blocks: []llm.Block{call}}
		out <- llm.AgentRuntimeEvent{Assistant: &assistant}
		result := make(chan llm.Block, 1)
		out <- llm.AgentRuntimeEvent{Tool: &llm.AgentRuntimeToolRequest{Call: call, Result: result}}
		<-result
		final := llm.Message{Role: llm.RoleAssistant, Model: req.Model, Blocks: []llm.Block{llm.TextBlock("finished")}}
		out <- llm.AgentRuntimeEvent{Assistant: &final}
		out <- llm.AgentRuntimeEvent{Done: true}
	}()
	return out, nil
}

func TestWholeTurnRuntimeDispatchesToolsThroughAgent(t *testing.T) {
	tool := &runtimeTool{}
	reg := tools.NewRegistry(tool)
	dir := t.TempDir()
	a := New(Options{Provider: &fakeProvider{}, Runtime: scriptedRuntime{}, Model: "sonnet", Cwd: dir, Tools: reg,
		Perms: permission.NewChecker(permission.ModeYolo, permission.Rules{}, dir)})
	events := drain(a.Run(context.Background(), "do the operation"), PermissionReply{})
	if tool.calls != 1 {
		t.Fatalf("tool calls = %d, want 1", tool.calls)
	}
	var assistantCount int
	for _, event := range events {
		if event.Kind == EvAssistant {
			assistantCount++
		}
	}
	if assistantCount != 2 {
		t.Fatalf("assistant boundaries = %d, want 2", assistantCount)
	}
	if got := a.messages; len(got) < 4 || len(got[1].ToolUses()) != 1 || got[2].Blocks[0].Content != "tool result" || got[3].Text() != "finished" {
		t.Fatalf("runtime transcript = %+v", got)
	}
}

// sleeper sleeps briefly and records how many calls ran at once.
type sleeper struct {
	name             string
	readOnly         bool
	active, maxSeen  *atomic.Int32
	writerSawCompany *atomic.Bool
}

func (s sleeper) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: s.name, Description: "sleeps", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (s sleeper) ReadOnly() bool { return s.readOnly }
func (s sleeper) Run(context.Context, *tools.Env, json.RawMessage) tools.Result {
	n := s.active.Add(1)
	defer s.active.Add(-1)
	for {
		m := s.maxSeen.Load()
		if n <= m || s.maxSeen.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(200 * time.Millisecond)
	if !s.readOnly && s.active.Load() > 1 {
		s.writerSawCompany.Store(true)
	}
	return tools.Result{Content: s.name + " done"}
}

// burstRuntime asks for all of calls at once, as Claude Code does for
// read-only MCP tools, and finishes when every result is back.
type burstRuntime struct {
	calls    []llm.Block
	parallel *[]string
}

func (b burstRuntime) Run(ctx context.Context, req llm.AgentRuntimeRequest) (<-chan llm.AgentRuntimeEvent, error) {
	*b.parallel = req.Parallel
	out := make(chan llm.AgentRuntimeEvent, len(b.calls)+3)
	go func() {
		defer close(out)
		assistant := llm.Message{Role: llm.RoleAssistant, Model: req.Model, Blocks: b.calls}
		out <- llm.AgentRuntimeEvent{Assistant: &assistant}
		var results []chan llm.Block
		for _, call := range b.calls {
			r := make(chan llm.Block, 1)
			results = append(results, r)
			out <- llm.AgentRuntimeEvent{Tool: &llm.AgentRuntimeToolRequest{Call: call, Result: r}}
		}
		for _, r := range results {
			<-r
		}
		final := llm.Message{Role: llm.RoleAssistant, Model: req.Model, Blocks: []llm.Block{llm.TextBlock("finished")}}
		out <- llm.AgentRuntimeEvent{Assistant: &final}
		out <- llm.AgentRuntimeEvent{Done: true}
	}()
	return out, nil
}

func TestRuntimeToolCallsRunInParallel(t *testing.T) {
	var active, maxSeen atomic.Int32
	var company atomic.Bool
	mk := func(name string, ro bool) sleeper { return sleeper{name, ro, &active, &maxSeen, &company} }
	reg := tools.NewRegistry(mk("look_a", true), mk("look_b", true), mk("change", false))
	call := func(id, name string) llm.Block {
		return llm.Block{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(`{}`)}
	}
	run := func(calls ...llm.Block) (*Agent, []string, time.Duration) {
		var parallel []string
		dir := t.TempDir()
		a := New(Options{Provider: &fakeProvider{}, Runtime: burstRuntime{calls, &parallel}, Model: "sonnet", Cwd: dir, Tools: reg,
			Perms: permission.NewChecker(permission.ModeYolo, permission.Rules{}, dir)})
		start := time.Now()
		drain(a.Run(context.Background(), "go"), PermissionReply{})
		return a, parallel, time.Since(start)
	}

	a, parallel, took := run(call("1", "look_a"), call("2", "look_b"), call("3", "look_a"))
	if strings.Join(parallel, ",") != "look_a,look_b" {
		t.Errorf("parallel tools sent to the runtime = %v", parallel)
	}
	if maxSeen.Load() != 3 || took > 550*time.Millisecond {
		t.Fatalf("three read-only calls: %d at once, %s; they should overlap", maxSeen.Load(), took)
	}
	answered := map[string]bool{}
	for _, m := range a.messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult && strings.HasSuffix(b.Content, "done") {
				answered[b.ID] = true
			}
		}
	}
	if len(answered) != 3 {
		t.Fatalf("results recorded for %v, want all three calls", answered)
	}

	// A call that writes runs alone, even when asked for alongside others.
	maxSeen.Store(0)
	run(call("1", "look_a"), call("2", "change"), call("3", "look_b"))
	if company.Load() {
		t.Fatal("a writing call overlapped another call")
	}
}

// answeringRuntime reports resolved as the concrete model for the turn, when
// it is not empty, and always sends usage.
type answeringRuntime struct{ resolved string }

func (r answeringRuntime) Run(ctx context.Context, req llm.AgentRuntimeRequest) (<-chan llm.AgentRuntimeEvent, error) {
	out := make(chan llm.AgentRuntimeEvent, 4)
	go func() {
		defer close(out)
		if r.resolved != "" {
			out <- llm.AgentRuntimeEvent{ResolvedModel: r.resolved}
		}
		answer := llm.Message{Role: llm.RoleAssistant, Model: req.Model, Blocks: []llm.Block{llm.TextBlock("done")}}
		out <- llm.AgentRuntimeEvent{Assistant: &answer}
		out <- llm.AgentRuntimeEvent{Usage: &llm.Usage{Input: 1000, Output: 10}}
		out <- llm.AgentRuntimeEvent{Done: true}
	}()
	return out, nil
}

// A rolling alias such as "sonnet" is in no catalog, so without the model the
// runtime resolved it to, the agent would measure context against the default
// window and report a misleading share.
func TestRuntimeResolvedModelSuppliesTheContextWindow(t *testing.T) {
	run := func(resolved string) *Agent {
		dir := t.TempDir()
		a := New(Options{Provider: &fakeProvider{}, Runtime: answeringRuntime{resolved}, Model: "sonnet",
			Cwd: dir, Tools: tools.NewRegistry(), Perms: permission.NewChecker(permission.ModeYolo, permission.Rules{}, dir)})
		drain(a.Run(context.Background(), "go"), PermissionReply{})
		return a
	}
	want := llm.Catalog["claude-sonnet-5"].ContextWindow
	for _, resolved := range []string{"claude-sonnet-5", "claude-sonnet-5-20260514"} {
		if got := run(resolved).Stats().ContextWindow; got != want {
			t.Errorf("window for resolved %q = %d, want %d", resolved, got, want)
		}
	}
	// Nothing to learn from: the catalog default stands, as before.
	if got := run("").Stats().ContextWindow; got != llm.DefaultContextWindow {
		t.Errorf("window without a resolved model = %d, want the default %d", got, llm.DefaultContextWindow)
	}
	if got := run("sonnet-ultra-9").Stats().ContextWindow; got != llm.DefaultContextWindow {
		t.Errorf("window for an unknown resolved model = %d, want the default %d", got, llm.DefaultContextWindow)
	}
	// The turn bills a Claude subscription, not tokens, so the alias stays
	// the priced model and its unknown price keeps cost unreported.
	a := run("claude-sonnet-5")
	if st := a.Stats(); st.CostUSD != 0 {
		t.Errorf("cost = %v, want 0: the resolved model must not price a subscription turn", st.CostUSD)
	}
	if a.messages[1].Model != "sonnet" {
		t.Errorf("transcript model = %q, want the selected name", a.messages[1].Model)
	}
	// Switching model drops what was learned for the old one.
	a.SetModel(&fakeProvider{}, "opus")
	if got := a.Stats().ContextWindow; got != llm.DefaultContextWindow {
		t.Errorf("window after switching model = %d, want the default %d", got, llm.DefaultContextWindow)
	}
}
