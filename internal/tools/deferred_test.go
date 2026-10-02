package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"larik/internal/llm"
)

// stubTool is a tool with a given name, description and schema.
type stubTool struct{ name, desc, schema string }

func (s stubTool) Spec() llm.ToolSpec {
	sch := s.schema
	if sch == "" {
		sch = `{"type":"object","properties":{}}`
	}
	return llm.ToolSpec{Name: s.name, Description: s.desc, Schema: json.RawMessage(sch)}
}
func (stubTool) ReadOnly() bool { return false }
func (s stubTool) Run(_ context.Context, _ *Env, input json.RawMessage) Result {
	return Result{Content: s.name + " ran with " + string(input)}
}

func names(specs []llm.ToolSpec) string {
	var out []string
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return strings.Join(out, ",")
}

var issueSchema = `{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}`

func deferredRegistry() *Registry {
	return NewRegistry(stubTool{name: "read"}).Defer(
		stubTool{"mcp__github__create_issue", "Create an issue in a GitHub repository", issueSchema},
		stubTool{"mcp__github__list_pulls", "List pull requests", ""},
		stubTool{"mcp__slack__send_message", "Send a message to a Slack channel", ""},
	)
}

func TestDeferKeepsToolsOutOfTheRequest(t *testing.T) {
	r := deferredRegistry()
	if got := names(r.Specs()); got != "read,tool_search,call_tool" {
		t.Fatalf("specs sent to the model: %s", got)
	}
	if _, ok := r.Get("mcp__slack__send_message"); !ok || !r.IsDeferred("mcp__slack__send_message") || r.IsDeferred("read") {
		t.Error("a deferred tool is still found by name, and marked as deferred")
	}
	// The index of what can be searched is in tool_search's description.
	desc := r.Specs()[1].Description
	for _, want := range []string{"- mcp__github__create_issue: Create an issue in a GitHub repository", "- mcp__slack__send_message: Send a message"} {
		if !strings.Contains(desc, want) {
			t.Errorf("tool_search's description lacks %q:\n%s", want, desc)
		}
	}
	// Adding a tool afterwards keeps the deferred ones.
	w := r.With(stubTool{name: "skill"})
	if got := names(w.Specs()); got != "read,tool_search,call_tool,skill" || len(w.Deferred()) != 3 {
		t.Errorf("With: %s, %d deferred", got, len(w.Deferred()))
	}
	if _, ok := w.Get("mcp__github__list_pulls"); !ok {
		t.Error("With lost a deferred tool")
	}
	// Nothing to defer changes nothing.
	plain := NewRegistry(stubTool{name: "read"})
	if plain.Defer() != plain {
		t.Error("Defer with no tools should return the registry as is")
	}
}

func TestToolSearch(t *testing.T) {
	r := deferredRegistry()
	search, _ := r.Get(ToolSearchName)
	run := func(input string) Result { return search.Run(context.Background(), nil, json.RawMessage(input)) }

	out := run(`{"query":"create github issue"}`)
	if out.IsError || !strings.Contains(out.Content, `<tool name="mcp__github__create_issue">`) || !strings.Contains(out.Content, `Input schema: {"properties":{"title":{"type":"string"}},"required":["title"],"type":"object"}`) {
		t.Fatalf("keyword search should return the definition: %s", out.Content)
	}
	// The best match leads: three words hit create_issue, one hits list_pulls.
	if i, j := strings.Index(out.Content, "create_issue"), strings.Index(out.Content, "list_pulls"); j >= 0 && j < i {
		t.Errorf("ranking: %s", out.Content)
	}
	if strings.Contains(out.Content, "slack") {
		t.Errorf("an unrelated tool was returned: %s", out.Content)
	}
	if out := run(`{"query":"select:mcp__slack__send_message, mcp__nope__x"}`); !strings.Contains(out.Content, `<tool name="mcp__slack__send_message">`) || !strings.Contains(out.Content, "Not found: mcp__nope__x") || out.Display != "mcp__slack__send_message" {
		t.Errorf("select: %s", out.Content)
	}
	if out := run(`{"query":"message","max_results":1}`); strings.Count(out.Content, "<tool name=") != 1 {
		t.Errorf("max_results: %s", out.Content)
	}
	if out := run(`{"query":"kubernetes"}`); out.IsError || !strings.Contains(out.Content, "No tools match") {
		t.Errorf("no match: %+v", out)
	}
	if out := run(`{"query":"  "}`); !out.IsError {
		t.Error("an empty query should fail")
	}
}

func TestToolSearchIndexShrinks(t *testing.T) {
	var many []Tool
	for i := 0; i < 300; i++ {
		many = append(many, stubTool{name: fmt.Sprintf("mcp__server%d__tool_%03d", i%4, i), desc: strings.Repeat("does something useful ", 5)})
	}
	desc := NewRegistry().Defer(many...).Specs()[0].Description
	if len(desc) > indexBudget+1500 {
		t.Fatalf("the index is %d bytes; it should fit the budget", len(desc))
	}
	if !strings.Contains(desc, "mcp__server0__ (75 tools)") || !strings.Contains(desc, "tool_000") {
		t.Errorf("a large set should be listed by server, names only:\n%s", desc[:600])
	}
}

func TestResolveCall(t *testing.T) {
	r := deferredRegistry()
	tool, args, err := r.ResolveCall(json.RawMessage(`{"tool":"mcp__github__create_issue","arguments":{"title":"Bug"}}`))
	if err != nil || tool.Spec().Name != "mcp__github__create_issue" || string(args) != `{"title":"Bug"}` {
		t.Fatalf("resolve: %v %s", err, args)
	}
	// Arguments sent as a JSON string, or left out.
	if _, args, err := r.ResolveCall(json.RawMessage(`{"tool":"mcp__github__create_issue","arguments":"{\"title\":\"Bug\"}"}`)); err != nil || string(args) != `{"title":"Bug"}` {
		t.Errorf("string arguments: %v %s", err, args)
	}
	if _, args, err := r.ResolveCall(json.RawMessage(`{"tool":"mcp__github__list_pulls"}`)); err != nil || string(args) != `{}` {
		t.Errorf("no arguments: %v %s", err, args)
	}
	for input, want := range map[string]string{
		`{"tool":"mcp__github__nope","arguments":{}}`: "Find the exact name with tool_search",
		`{"tool":"read","arguments":{}}`:              "regular tool",
		`{"tool":"call_tool","arguments":{}}`:         "regular tool",
		`{"arguments":{}}`:                            "INVALID_JSON",
		`nonsense`:                                    "INVALID_JSON",
	} {
		if _, _, err := r.ResolveCall(json.RawMessage(input)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", input, err)
		}
	}
}

func TestIndexLineDropsMCPTag(t *testing.T) {
	sp := llm.ToolSpec{Name: "mcp__gh__create_issue", Description: `[MCP server "gh"] Create an issue in a repository`}
	if got := indexLine(sp); got != "Create an issue in a repository" {
		t.Fatalf("got %q", got)
	}
	sp = llm.ToolSpec{Name: "local", Description: `[MCP server "x"] not from MCP`}
	if got := indexLine(sp); got != sp.Description {
		t.Fatalf("a non-MCP tool's description must stay: %q", got)
	}
}
