package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/tools"
)

const todoInput = `{"todos":[{"content":"Read the code","status":"completed"},{"content":"Fix the bug","status":"in_progress","active_form":"Fixing the bug"},{"content":"Run the tests","status":"pending"}]}`

func todoEnd(input string, sub string) agent.Event {
	return agent.Event{Kind: agent.EvToolEnd, ToolID: "t1", ToolName: tools.TodoToolName, Input: json.RawMessage(input), Agent: sub}
}

func TestTodosPinnedWhileRunning(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 100, 40
	card := plain(printed(m.handleEvent(todoEnd(todoInput, ""))))
	for _, want := range []string{"todos(1/3 done)", "✔ Read the code", "◼ Fix the bug", "◻ Run the tests"} {
		if !strings.Contains(card, want) {
			t.Errorf("card lacks %q:\n%s", want, card)
		}
	}
	m.running = true
	live := plain(m.liveView())
	if !strings.Contains(live, "Tasks · 1/3 done") || !strings.Contains(live, "Fixing the bug…") {
		t.Fatalf("live view:\n%s", live)
	}

	m.handleEvent(todoEnd(`{"todos":[{"content":"Read the code","status":"completed"}]}`, ""))
	if strings.Contains(plain(m.liveView()), "Tasks ·") {
		t.Error("a finished list shouldn't stay pinned")
	}
	m.handleEvent(todoEnd(todoInput, "worker: x"))
	if len(m.todos) != 1 {
		t.Error("a subagent's list must not replace the main one")
	}
}

func TestTodosRestoredAndCleared(t *testing.T) {
	m := testModel(t)
	history := []llm.Message{{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "t1", Name: tools.TodoToolName, Input: json.RawMessage(todoInput)}}}}
	m.todos, _ = tools.LatestTodos(history)
	if out := plain(printed(m.command("/todos"))); !strings.Contains(out, "Fix the bug") {
		t.Fatalf("/todos: %q", out)
	}
	m.command("/clear")
	if out := plain(printed(m.command("/todos"))); !strings.Contains(out, "no task list") {
		t.Fatalf("/todos after /clear: %q", out)
	}
}

func TestLongTodoListKeepsCurrentInView(t *testing.T) {
	m := testModel(t)
	var items []string
	for i := range 20 {
		status := "completed"
		if i == 12 {
			status = "in_progress"
		} else if i > 12 {
			status = "pending"
		}
		items = append(items, `{"content":"step `+string(rune('a'+i))+`","status":"`+status+`"}`)
	}
	todos, err := tools.ParseTodos(json.RawMessage(`{"todos":[` + strings.Join(items, ",") + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	lines := plain(strings.Join(m.todoLines(todos, 5), "\n"))
	if !strings.Contains(lines, "◼ step m") || !strings.Contains(lines, "earlier") || !strings.Contains(lines, "more") {
		t.Fatalf("window:\n%s", lines)
	}
}
