package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/tools"
)

// remoteTool stands in for an MCP tool.
type remoteTool struct{ ran *[]string }

func (remoteTool) ReadOnly() bool { return false }
func (remoteTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: "mcp__tracker__create_issue", Description: "Create an issue", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (r remoteTool) Run(_ context.Context, _ *tools.Env, input json.RawMessage) tools.Result {
	*r.ran = append(*r.ran, string(input))
	return tools.Result{Content: "created #7"}
}

func deferredAgent(t *testing.T, mode permission.Mode, rules permission.Rules, script ...llm.Message) (*Agent, *fakeProvider, *[]string) {
	t.Helper()
	var ran []string
	fp := &fakeProvider{script: script}
	dir := t.TempDir()
	a := New(Options{Provider: fp, Model: "m", Cwd: dir,
		Tools: tools.NewRegistry().Defer(remoteTool{&ran}),
		Perms: permission.NewChecker(mode, rules, dir)})
	return a, fp, &ran
}

func TestCallToolRunsTheDeferredToolUnderItsOwnName(t *testing.T) {
	call := `{"tool":"mcp__tracker__create_issue","arguments":{"title":"Bug"}}`
	a, fp, ran := deferredAgent(t, permission.ModeDefault, permission.Rules{},
		assistant(toolUse("1", tools.CallToolName, call)),
		assistant(llm.TextBlock("done")))
	evs := drain(a.Run(context.Background(), "file it"), PermissionReply{Allow: true})

	if len(*ran) != 1 || (*ran)[0] != `{"title":"Bug"}` {
		t.Fatalf("the deferred tool should get its own arguments: %v", *ran)
	}
	// The user is asked about the real tool, with a rule for it.
	asked := permissionEvents(evs)
	if len(asked) != 1 || asked[0].ToolName != "mcp__tracker__create_issue" || asked[0].SuggestedRule != "mcp__tracker__create_issue" || string(asked[0].Input) != `{"title":"Bug"}` {
		t.Fatalf("permission request: %+v", asked)
	}
	for _, e := range evs {
		if (e.Kind == EvToolStart || e.Kind == EvToolEnd) && e.ToolName != "mcp__tracker__create_issue" {
			t.Errorf("%s names %q; front ends should see the real tool", e.Kind, e.ToolName)
		}
	}
	// The result goes back under the name the model called.
	msgs := fp.requests[1].Messages
	res := msgs[len(msgs)-1].Blocks[0]
	if res.Type != llm.BlockToolResult || res.Name != tools.CallToolName || res.ID != "1" || res.Content != "created #7" {
		t.Errorf("tool result sent to the model: %+v", res)
	}
	// Only the two fixed tools were ever declared.
	if specs := fp.requests[0].Tools; len(specs) != 2 || specs[0].Name != tools.ToolSearchName || specs[1].Name != tools.CallToolName {
		t.Errorf("declared tools: %+v", specs)
	}
}

func TestCallToolFollowsTheToolsRules(t *testing.T) {
	call := `{"tool":"mcp__tracker__create_issue","arguments":{}}`
	// A deny rule on the server blocks it through the wrapper too.
	a, _, ran := deferredAgent(t, permission.ModeYolo, permission.Rules{Deny: []string{"mcp__tracker"}},
		assistant(toolUse("1", tools.CallToolName, call)), assistant(llm.TextBlock("ok")))
	drain(a.Run(context.Background(), "go"), PermissionReply{})
	if res := lastToolResult(t, a); len(*ran) != 0 || !res.IsError || !strings.Contains(res.Content, "denied by rule") || res.Name != tools.CallToolName {
		t.Errorf("a deny rule on the tool should hold: ran=%v %+v", *ran, res)
	}
	// An allow rule lets it run without asking.
	a, _, ran = deferredAgent(t, permission.ModeDefault, permission.Rules{Allow: []string{"mcp__tracker__create_issue"}},
		assistant(toolUse("1", tools.CallToolName, call)), assistant(llm.TextBlock("ok")))
	if evs := drain(a.Run(context.Background(), "go"), PermissionReply{}); len(permissionEvents(evs)) != 0 || len(*ran) != 1 {
		t.Errorf("an allow rule on the tool should apply: ran=%v", *ran)
	}
	// Plan mode blocks a tool that isn't read-only.
	a, _, ran = deferredAgent(t, permission.ModePlan, permission.Rules{},
		assistant(toolUse("1", tools.CallToolName, call)), assistant(llm.TextBlock("ok")))
	drain(a.Run(context.Background(), "go"), PermissionReply{Allow: true})
	if len(*ran) != 0 {
		t.Error("plan mode should block the deferred tool")
	}
	// An unknown name is an error for the model to fix, not a crash.
	a, _, _ = deferredAgent(t, permission.ModeYolo, permission.Rules{},
		assistant(toolUse("1", tools.CallToolName, `{"tool":"mcp__tracker__nope","arguments":{}}`)), assistant(llm.TextBlock("ok")))
	drain(a.Run(context.Background(), "go"), PermissionReply{})
	if res := lastToolResult(t, a); !res.IsError || !strings.Contains(res.Content, "tool_search") {
		t.Errorf("unknown deferred tool: %+v", res)
	}
}
