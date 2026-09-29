package session

import (
	"encoding/json"
	"strings"
	"testing"

	"larik/internal/llm"
)

func TestMarkdown(t *testing.T) {
	long := strings.Repeat("line\n", 50)
	st := &State{
		Meta: Meta{Cwd: "/w", Provider: "anthropic", Model: "claude-opus-5"},
		All: []llm.Message{
			{Role: llm.RoleUser, Blocks: []llm.Block{
				llm.TextBlock("<system-note>\nPlan mode is on.\n</system-note>\n\nfix the ```weird``` bug"),
				{Type: llm.BlockText, Text: "<file path=\"a.go\">secret contents</file>", Attachment: "a.go"},
			}},
			{Role: llm.RoleAssistant, Model: "claude-opus-5", Blocks: []llm.Block{
				{Type: llm.BlockThinking, Text: "hidden reasoning"},
				llm.TextBlock("Let me look."),
				{Type: llm.BlockToolUse, ID: "t1", Name: "bash", Input: json.RawMessage(`{"command":"go test ./..."}`)},
			}},
			{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ID: "t1", Content: long, IsError: true}}},
			{Role: llm.RoleUser, Blocks: []llm.Block{llm.TextBlock("<task-notification id=\"bg1\" agent=\"a\" status=\"done\">\nok\n</task-notification>")}},
		},
	}
	md := Markdown(st, "20260929-abc")
	for _, want := range []string{
		"# Larik session 20260929-abc", "Model: anthropic/claude-opus-5",
		"## User\n\nfix the ```weird``` bug", "_Attached: `a.go`_",
		"## Assistant · claude-opus-5", "Let me look.", "**bash**", `"command": "go test ./..."`,
		"<summary>Error</summary>", "(20 more lines)", "## Larik",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("export lacks %q:\n%s", want, md)
		}
	}
	for _, not := range []string{"Plan mode is on", "secret contents", "hidden reasoning"} {
		if strings.Contains(md, not) {
			t.Errorf("export should leave out %q", not)
		}
	}
}
