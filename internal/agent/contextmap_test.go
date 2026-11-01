package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"larik/internal/llm"
)

func TestContextMapSortsTheContextIntoParts(t *testing.T) {
	text := func(n int) string { return strings.Repeat("x", n) }
	a := &Agent{}
	a.opts.System = text(400)
	a.lastContext = 5000
	a.messages = []llm.Message{
		{Role: llm.RoleUser, Blocks: []llm.Block{
			llm.TextBlock(text(40)),
			{Type: llm.BlockText, Text: text(800), Attachment: "notes.md"},
			{Type: llm.BlockImage, Data: text(1 << 20)},
		}},
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockThinking, Text: text(80)},
			llm.TextBlock(text(120)),
			{Type: llm.BlockToolUse, ID: "1", Name: "read", Input: json.RawMessage(`{"path":"big.go"}`)},
			{Type: llm.BlockToolUse, ID: "2", Name: "read", Input: json.RawMessage(`{"path":"small.go"}`)},
		}},
		{Role: llm.RoleUser, Blocks: []llm.Block{
			{Type: llm.BlockToolResult, ID: "2", Name: "read", Content: text(40)},
			{Type: llm.BlockToolResult, ID: "1", Name: "read", Content: text(4000), Images: []llm.Block{{Type: llm.BlockImage}}},
		}},
	}
	got := a.ContextMap()
	if got.Parts[0].Name != "images" || got.Parts[0].Weight != 2*imageTokens {
		t.Fatalf("an image counts as a picture, not its bytes: %+v", got.Parts)
	}
	weights := map[string]int{}
	sum := 0
	for _, p := range got.Parts {
		weights[p.Name] = p.Weight
		sum += p.Weight
	}
	for name, want := range map[string]int{"system prompt": 100, "your prompts": 10, "attached files": 200, "thinking": 20, "replies": 30} {
		if weights[name] != want {
			t.Errorf("%s = %d, want %d", name, weights[name], want)
		}
	}
	if weights["read"] < 1010 {
		t.Errorf("read's calls and results = %d", weights["read"])
	}
	if got.Total != sum || got.Tokens != 5000 {
		t.Errorf("total %d, sum %d, tokens %d", got.Total, sum, got.Tokens)
	}
	if len(got.Largest) != 2 || !strings.Contains(string(got.Largest[0].Input), "big.go") {
		t.Errorf("largest: %+v", got.Largest)
	}
}
