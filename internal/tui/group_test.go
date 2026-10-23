package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/tools"
)

func readEnd(id, path string) agent.Event {
	return agent.Event{Kind: agent.EvToolEnd, ToolID: id, ToolName: "read", Input: json.RawMessage(`{"path":"` + path + `"}`), Output: "     1\tx"}
}

func TestReadOnlyRunPrintsAsOneCard(t *testing.T) {
	m := testModel(t)
	for _, e := range []agent.Event{
		readEnd("r1", "a.go"),
		readEnd("r2", "b.go"),
		readEnd("r3", "a.go"), // read again: still one file
		{Kind: agent.EvToolEnd, ToolID: "g1", ToolName: "grep", Input: json.RawMessage(`{"pattern":"TODO"}`), Output: "a.go:1:x"},
		// A reply that only calls tools doesn't end the run.
		{Kind: agent.EvAssistant, Message: &llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, Name: "read"}}}},
	} {
		if out := printed(m.handleEvent(e)); out != "" {
			t.Fatalf("a call in the run printed on its own: %q", plain(out))
		}
	}
	if live := plain(m.liveView()); !strings.Contains(live, "✓ Read 2 files · searched 1 pattern") {
		t.Fatalf("the live view should show the run while it waits:\n%s", live)
	}

	// Text from the model ends the run, which prints first.
	reply := agent.Event{Kind: agent.EvAssistant, Message: &llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{llm.TextBlock("Found it.")}}}
	if !m.endsGroup(reply) {
		t.Fatal("a reply with text should end the run")
	}
	grouped := m.grouped
	if cmd := m.handleEvent(reply); cmd == nil || len(m.grouped) != 0 {
		t.Fatalf("the run should print before the reply; %d calls left", len(m.grouped))
	}
	card := plain(m.renderGroupCard(grouped))
	for _, want := range []string{"● Read 2 files · searched 1 pattern", "⎿ read(a.go), read(b.go), read(a.go), grep(TODO)"} {
		if !strings.Contains(card, want) {
			t.Errorf("card lacks %q:\n%s", want, card)
		}
	}
}

func TestARunOfOneKeepsItsCard(t *testing.T) {
	m := testModel(t)
	m.handleEvent(readEnd("r1", "a.go"))
	if got := plain(printed(m.flushGroup())); !strings.HasPrefix(got, "● read(a.go)") || strings.Contains(got, "Read 1 file") {
		t.Fatalf("a lone call should print its own card: %q", got)
	}
	if printed(m.flushGroup()) != "" {
		t.Fatal("nothing should be left to print")
	}
}

func TestWhatEndsOrSkipsARun(t *testing.T) {
	m := testModel(t)
	edit := agent.Event{Kind: agent.EvToolEnd, ToolID: "e1", ToolName: "edit", Input: json.RawMessage(`{"path":"a.go"}`), Display: "-a\n+b"}
	failed := readEnd("r9", "missing.go")
	failed.IsError = true
	for name, e := range map[string]agent.Event{"an edit": edit, "a failed read": failed} {
		if m.groupable(e) || !m.endsGroup(e) {
			t.Errorf("%s should print its own card and end the run", name)
		}
	}
	sub := readEnd("s1", "a.go")
	sub.Agent = "explore: x"
	if m.groupable(sub) {
		t.Error("a subagent's calls keep their nested lines")
	}
	// A script's calls belong to its card, and don't end a run either.
	m.handleEvent(agent.Event{Kind: agent.EvToolStart, ToolID: "c1", ToolName: tools.CodeToolName, Input: scriptInput("1")})
	if child := readEnd("c1.1", "a.go"); m.groupable(child) || m.endsGroup(child) {
		t.Error("a script's call should go to the script's card")
	}
	for _, mode := range []string{"compact", "verbose"} {
		m.appearance = mode
		if m.groupable(readEnd("r1", "a.go")) {
			t.Errorf("%s mode shouldn't group", mode)
		}
	}
}

func TestRunPrintsWhenTheTurnEnds(t *testing.T) {
	m := testModel(t)
	m.handleEvent(readEnd("r1", "a.go"))
	m.handleEvent(readEnd("r2", "b.go"))
	m.Update(runEndedMsg{})
	if len(m.grouped) != 0 {
		t.Fatal("a run the turn ended on should print")
	}
}

func TestGroupSummaryWords(t *testing.T) {
	calls := []agent.Event{
		{ToolName: "web_search", Input: json.RawMessage(`{"query":"a"}`)},
		{ToolName: "web_search", Input: json.RawMessage(`{"query":"b"}`)},
		{ToolName: "web_fetch", Input: json.RawMessage(`{"url":"u"}`)},
		{ToolName: "glob", Input: json.RawMessage(`{"pattern":"*.go"}`)},
	}
	if got := groupSummary(calls); got != "Searched the web 2 times · fetched 1 page · listed 1 glob" {
		t.Fatalf("summary = %q", got)
	}
}
