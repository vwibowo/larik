package agent

import (
	"context"
	"encoding/json"
	"testing"

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
