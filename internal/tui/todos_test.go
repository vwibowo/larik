package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

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
	for _, want := range []string{"todos(1/3 done)"} {
		if !strings.Contains(card, want) {
			t.Errorf("card lacks %q:\n%s", want, card)
		}
	}
	m.running = true
	live := plain(m.liveView())
	if !strings.Contains(live, "Fixing the bug…") || strings.Contains(live, "Tasks ·") {
		t.Fatalf("task checklist should float separately from live status:\n%s", live)
	}
	floating := plain(m.View().Content)
	if !strings.Contains(floating, "Tasks · 1/3 done") || !strings.Contains(floating, "Fix the bug") {
		t.Fatalf("floating card should show active tasks:\n%s", floating)
	}

	completed := plain(printed(m.handleEvent(todoEnd(`{"todos":[{"content":"Read the code","status":"completed"}]}`, ""))))
	if !strings.Contains(completed, "Tasks complete · 1/1 done") || !strings.Contains(completed, "✔ Read the code") {
		t.Fatalf("completed list should move to conversation once:\n%s", completed)
	}
	if strings.Contains(plain(m.liveView()), "Tasks ·") {
		t.Error("a finished list shouldn't stay pinned")
	}
	repeated := plain(printed(m.handleEvent(todoEnd(`{"todos":[{"content":"Read the code","status":"completed"}]}`, ""))))
	if strings.Contains(repeated, "Tasks complete") {
		t.Fatalf("completed task summary should not be repeated:\n%s", repeated)
	}
	m.handleEvent(todoEnd(todoInput, "worker: x"))
	if len(m.todos) != 1 {
		t.Error("a subagent's list must not replace the main one")
	}
}

func TestTodosUseCompactFallbackOnNarrowTerminals(t *testing.T) {
	m := testModel(t)
	m.todos, _ = tools.ParseTodos(json.RawMessage(todoInput))
	m.running = true
	m.width, m.height = 50, 20
	live := plain(m.liveView())
	if !strings.Contains(live, "Tasks · 1/3 done") {
		t.Fatalf("narrow terminal should keep task status in the live area:\n%s", live)
	}
	if got := lipgloss.Width(plain(strings.Join(m.liveTodos(), "\n"))); got > 50 {
		t.Fatalf("narrow task fallback exceeds terminal width: %d", got)
	}
}

func TestTodosMoveIntoInfoSidebar(t *testing.T) {
	m := testModel(t)
	m.todos, _ = tools.ParseTodos(json.RawMessage(todoInput))
	m.showInfo = true
	m.width, m.height = 120, 32
	view := plain(m.View().Content)
	if !strings.Contains(view, "1/3 done") || !strings.Contains(view, "F2 to hide") {
		t.Fatalf("wide info sidebar should include tasks and session details:\n%s", view)
	}
	if m.view.Width() >= m.width {
		t.Fatalf("conversation viewport should narrow beside the sidebar: width=%d terminal=%d", m.view.Width(), m.width)
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
