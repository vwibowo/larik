package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"larik/internal/llm"
)

func TestTodoWrite(t *testing.T) {
	r := run(t, TodoWrite{}, nil, `{"todos":[{"content":"Read the code","status":"completed"},{"content":"Fix the bug","status":"in_progress","active_form":"Fixing the bug"},{"content":"Run the tests","status":"pending"}]}`)
	if r.IsError || !strings.Contains(r.Content, "1 of 3 done") || !strings.Contains(r.Content, "[x] Read the code\n[>] Fix the bug\n[ ] Run the tests") {
		t.Fatalf("got %+v", r)
	}
	for input, want := range map[string]string{
		`{"todos":[{"content":"a","status":"in_progress"},{"content":"b","status":"in_progress"}]}`: "exactly one in progress",
		`{"todos":[{"content":" ","status":"pending"}]}`:                                            "no content",
		`{"todos":[{"content":"a","status":"doing"}]}`:                                              "status must be",
		`{"todo":[]}`: "",
	} {
		r := run(t, TodoWrite{}, nil, input)
		if want == "" {
			if r.IsError || !strings.Contains(r.Content, "cleared") {
				t.Errorf("%s: %+v", input, r)
			}
			continue
		}
		if !r.IsError || !strings.Contains(r.Content, want) {
			t.Errorf("%s: got %+v, want an error with %q", input, r, want)
		}
	}
}

func TestLatestTodosSkipsFailedCalls(t *testing.T) {
	use := func(id, input string) llm.Block {
		return llm.Block{Type: llm.BlockToolUse, ID: id, Name: TodoToolName, Input: json.RawMessage(input)}
	}
	msgs := []llm.Message{
		{Role: llm.RoleAssistant, Blocks: []llm.Block{use("t1", `{"todos":[{"content":"first","status":"pending"}]}`)}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ID: "t1", Content: "ok"}}},
		{Role: llm.RoleAssistant, Blocks: []llm.Block{use("t2", `{"todos":[{"content":"x","status":"bogus"}]}`)}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ID: "t2", Content: "bad", IsError: true}}},
	}
	todos, ok := LatestTodos(msgs)
	if !ok || len(todos) != 1 || todos[0].Content != "first" {
		t.Fatalf("got %+v %v", todos, ok)
	}
	if _, ok := LatestTodos(msgs[:0]); ok {
		t.Fatal("no calls means no list")
	}
}
