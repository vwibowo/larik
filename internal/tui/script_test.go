package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/tools"
)

func scriptInput(code string) json.RawMessage {
	in, _ := json.Marshal(map[string]string{"code": code})
	return in
}

func TestScriptLabel(t *testing.T) {
	for code, want := range map[string]string{
		"// count the Go files\nconst n = 1":    "count the Go files",
		"\n\n  const files = tools.glob({})\nx": "const files = tools.glob({})",
		"/* sum sizes */\nlet s = 0":            "sum sizes",
		"   \n":                                 "",
	} {
		if got := scriptLabel(code); got != want {
			t.Errorf("scriptLabel(%q) = %q, want %q", code, got, want)
		}
	}
	if got := toolTitle(tools.CodeToolName, scriptInput("// count lines\nlet n = 0"), nil); got != "script(count lines)" {
		t.Errorf("title = %q", got)
	}
}

func TestScriptResultDropsTheCallCount(t *testing.T) {
	body, calls, ok := scriptResult("total 30\n[run_code: 3 tool calls]")
	if !ok || calls != 3 || body != "total 30" {
		t.Fatalf("got %q %d %v", body, calls, ok)
	}
	if body, calls, ok := scriptResult("[run_code: 1 tool call]"); !ok || calls != 1 || body != "" {
		t.Fatalf("output with only the count: %q %d %v", body, calls, ok)
	}
	if body, _, ok := scriptResult("no count here"); ok || body != "no count here" {
		t.Fatalf("output without the count should stay as it is: %q %v", body, ok)
	}
}

func TestScriptCallsPrintInsideItsCard(t *testing.T) {
	m := testModel(t)
	code := "// count lines in Go files\nconst files = tools.glob({pattern: \"*.go\"})\nlet n = 0\nfor (const f of files) n += 1\nconsole.log(`total ${n}`)"
	events := []agent.Event{
		{Kind: agent.EvToolStart, ToolID: "c1", ToolName: tools.CodeToolName, Input: scriptInput(code)},
		{Kind: agent.EvToolStart, ToolID: "c1.1", ToolName: "glob", Input: json.RawMessage(`{"pattern":"*.go"}`)},
		{Kind: agent.EvToolEnd, ToolID: "c1.1", ToolName: "glob", Input: json.RawMessage(`{"pattern":"*.go"}`), Output: "a.go\nb.go"},
		{Kind: agent.EvToolStart, ToolID: "c1.2", ToolName: "read", Input: json.RawMessage(`{"path":"a.go"}`)},
		{Kind: agent.EvToolEnd, ToolID: "c1.2", ToolName: "read", Input: json.RawMessage(`{"path":"a.go"}`), Output: "1\tpackage a"},
		{Kind: agent.EvToolStart, ToolID: "c1.3", ToolName: "read", Input: json.RawMessage(`{"path":"b.go"}`)},
	}
	for _, e := range events {
		if out := plain(printed(m.handleEvent(e))); out != "" {
			t.Fatalf("a script's call printed a card of its own: %q", out)
		}
	}
	// While it runs, the calls sit under the script.
	live := plain(m.liveView())
	if !strings.Contains(live, "script(count lines in Go files)") || !strings.Contains(live, "↳ ") || !strings.Contains(live, "read(b.go)") {
		t.Fatalf("live view should nest the running call under the script:\n%s", live)
	}

	for _, e := range []agent.Event{
		{Kind: agent.EvToolEnd, ToolID: "c1.3", ToolName: "read", Input: json.RawMessage(`{"path":"b.go"}`), Output: "1\tpackage b"},
		{Kind: agent.EvToolStart, ToolID: "c1.4", ToolName: "edit", Input: json.RawMessage(`{"path":"a.go"}`)},
		{Kind: agent.EvToolEnd, ToolID: "c1.4", ToolName: "edit", Input: json.RawMessage(`{"path":"a.go"}`), Output: "ok", Display: "-package a\n+package b"},
	} {
		if out := plain(printed(m.handleEvent(e))); out != "" {
			t.Fatalf("a script's call printed a card of its own: %q", out)
		}
	}
	card := plain(printed(m.handleEvent(agent.Event{Kind: agent.EvToolEnd, ToolID: "c1", ToolName: tools.CodeToolName, Input: scriptInput(code), Output: "total 2\n[run_code: 4 tool calls]"})))
	for _, want := range []string{
		"✓ script(count lines in Go files) · 4 calls",
		"│ const files = tools.glob",
		"… +1 line",
		"├ glob(*.go)",
		"├ read(a.go) +1 more",
		"├ edit(a.go)",
		"+package b",
		"⎿ total 2",
	} {
		if !strings.Contains(card, want) {
			t.Errorf("card lacks %q:\n%s", want, card)
		}
	}
	for _, unwanted := range []string{`{"code"`, "[run_code:", "read(b.go)"} {
		if strings.Contains(card, unwanted) {
			t.Errorf("card shows %q:\n%s", unwanted, card)
		}
	}
	if len(m.scripts) != 0 {
		t.Errorf("finished script still tracked: %v", m.scripts)
	}
}

func TestScriptCardShowsFailedCalls(t *testing.T) {
	m := testModel(t)
	code := "tools.read({path: \"missing\"})"
	m.handleEvent(agent.Event{Kind: agent.EvToolStart, ToolID: "c1", ToolName: tools.CodeToolName, Input: scriptInput(code)})
	m.handleEvent(agent.Event{Kind: agent.EvToolEnd, ToolID: "c1.1", ToolName: "read", Input: json.RawMessage(`{"path":"missing"}`), Output: "no such file", IsError: true})
	card := plain(printed(m.handleEvent(agent.Event{Kind: agent.EvToolEnd, ToolID: "c1", ToolName: tools.CodeToolName, Input: scriptInput(code), Output: "Error: read: no such file\n[run_code: 1 tool call]", IsError: true})))
	if !strings.Contains(card, "├ read(missing)") || !strings.Contains(card, "no such file") || !strings.Contains(card, "· 1 call") {
		t.Fatalf("a failed call should show its error inside the card:\n%s", card)
	}
}

func TestScriptCallsCapUnlessVerbose(t *testing.T) {
	m := testModel(t)
	var calls []agent.Event
	for _, name := range []string{"glob", "read", "grep", "bash", "glob", "read", "grep", "bash", "glob", "read", "grep", "bash"} {
		calls = append(calls, agent.Event{ToolName: name, Input: json.RawMessage(`{}`)})
	}
	lines := m.scriptCallLines(calls)
	if last := plain(lines[len(lines)-1]); !strings.Contains(last, "more call") || len(lines) > m.lines(8)+1 {
		t.Fatalf("a long list of calls should be cut short:\n%s", strings.Join(lines, "\n"))
	}
	m.verbose = true
	lines = m.scriptCallLines(calls)
	if n := strings.Count(plain(strings.Join(lines, "\n")), "├ "); n != len(calls) || strings.Contains(plain(strings.Join(lines, "\n")), "more call") {
		t.Fatalf("verbose shows every call, got %d:\n%s", n, strings.Join(lines, "\n"))
	}
}

func TestHistoryShowsScripts(t *testing.T) {
	m := testModel(t)
	code := "// tally the files\ntools.glob({pattern: \"*\"})"
	history := []llm.Message{
		llm.UserText("count them"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "c1", Name: tools.CodeToolName, Input: scriptInput(code)}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ID: "c1", Name: tools.CodeToolName, Content: "7\n[run_code: 2 tool calls]"}}},
	}
	got := plain(m.renderHistory(history, "resumed", 80))
	if !strings.Contains(got, "✓ script(tally the files) · 2 calls") || strings.Contains(got, `{"code"`) {
		t.Fatalf("history should show the script's title and calls:\n%s", got)
	}
}

func TestPermissionPromptNamesTheScript(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.handleEvent(agent.Event{Kind: agent.EvToolStart, ToolID: "c1", ToolName: tools.CodeToolName, Input: scriptInput("// tidy the imports\ntools.edit({})")})
	ev := agent.Event{Kind: agent.EvPermission, ToolID: "c1.1", ToolName: "edit", Input: json.RawMessage(`{"path":"a.go","old_string":"x","new_string":"y"}`)}
	m.perm = &ev
	if got := plain(m.permissionView()); !strings.Contains(got, "from script: tidy the imports") {
		t.Fatalf("a script's call should say which script asks:\n%s", got)
	}
	// A direct call says nothing about scripts.
	direct := agent.Event{Kind: agent.EvPermission, ToolID: "e1", ToolName: "edit", Input: ev.Input}
	m.perm = &direct
	if got := plain(m.permissionView()); strings.Contains(got, "script") {
		t.Fatalf("a direct call isn't from a script:\n%s", got)
	}
}
