package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"larik/internal/config"
	"larik/internal/llm"
	"larik/internal/tools"
)

type sizedTool struct {
	name string
	size int
}

func (s sizedTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: s.name, Description: strings.Repeat("d", s.size), Schema: json.RawMessage(`{}`)}
}
func (sizedTool) ReadOnly() bool { return false }
func (sizedTool) Run(context.Context, *tools.Env, json.RawMessage) tools.Result {
	return tools.Result{}
}

func toolSet(n, size int) []tools.Tool {
	var out []tools.Tool
	for i := 0; i < n; i++ {
		out = append(out, sizedTool{fmt.Sprintf("mcp__s__t%d", i), size})
	}
	return out
}

func TestDeferTools(t *testing.T) {
	few, many, heavy := toolSet(5, 100), toolSet(DeferCount+1, 10), toolSet(5, DeferChars/4)
	for _, c := range []struct {
		name    string
		setting string
		ts      []tools.Tool
		want    bool
	}{
		{"a few small tools stay in the request", "", few, false},
		{"auto is the default", "auto", few, false},
		{"many tools are deferred", "", many, true},
		{"a few tools with huge definitions are deferred", "", heavy, true},
		{"on defers any", "on", few, true},
		{"on with no tools has nothing to defer", "on", nil, false},
		{"off never defers", "off", many, false},
		{"an unknown value means auto", "sometimes", many, true},
	} {
		if got := deferTools(c.setting, c.ts); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

// With tool_search on, a real server's tools stay out of the request and
// are found and run through the two fixed tools.
func TestDeferredServerTools(t *testing.T) {
	bin := buildServer(t)
	cfg := newCfg(t, map[string]config.MCPServer{
		"local": {Command: bin, Env: map[string]string{"GREETING": "hi "}, Trusted: true},
	})
	cfg.ToolSearch = "on"
	m := NewManager(cfg, "test")
	m.Start()
	defer m.Close()

	var notices []string
	reg := m.Registry(context.Background(), func(s string) { notices = append(notices, s) })
	var names []string
	for _, s := range reg.Specs() {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, ","); got != "read,write,write_files,edit,bash,raw_output,grep,glob,todo_write,multi_edit,list_mcp_resources,read_mcp_resource,tool_search,call_tool" {
		t.Fatalf("tools sent to the model = %s", got)
	}
	if got := strings.Join(notices, "; "); !strings.Contains(got, "2 MCP tools are loaded on demand") {
		t.Errorf("notices: %s", got)
	}

	search, _ := reg.Get(tools.ToolSearchName)
	if !strings.Contains(search.Spec().Description, "- mcp__local__echo: ") {
		t.Errorf("tool_search should list the server's tools: %s", search.Spec().Description)
	}
	found := search.Run(context.Background(), nil, json.RawMessage(`{"query":"echo"}`))
	if found.IsError || !strings.Contains(found.Content, `<tool name="mcp__local__echo">`) || !strings.Contains(found.Content, `"text"`) {
		t.Fatalf("search result: %s", found.Content)
	}

	tool, args, err := reg.ResolveCall(json.RawMessage(`{"tool":"mcp__local__echo","arguments":{"text":"there"}}`))
	if err != nil {
		t.Fatal(err)
	}
	// The deferred tool keeps what the server said about it.
	if !tool.ReadOnly() {
		t.Error("the echo tool is read-only, deferred or not")
	}
	if out := tool.Run(context.Background(), nil, args); out.Content != "hi there" || out.IsError {
		t.Errorf("echo through call_tool = %q", out.Content)
	}
}
