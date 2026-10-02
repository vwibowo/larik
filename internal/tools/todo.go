package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"larik/internal/llm"
)

// TodoToolName is the name of the task-list tool.
const TodoToolName = "todo_write"

const maxTodos = 50

// Todo is one item of the model's task list.
type Todo struct {
	Content string `json:"content"`
	Status  string `json:"status"` // pending, in_progress or completed
	// ActiveForm is the item as an ongoing action ("Running the tests"),
	// shown while it is in progress.
	ActiveForm string `json:"active_form,omitempty"`
}

// Todo statuses.
const (
	TodoPending    = "pending"
	TodoInProgress = "in_progress"
	TodoCompleted  = "completed"
)

// TodoWrite replaces the model's task list. The list lives only in the
// transcript: the latest successful call's input is the current list, so
// front ends and resumed sessions read it from there and the tool keeps no
// state of its own.
type TodoWrite struct{}

// ReadOnly: it changes nothing on disk, so it never asks and works in
// plan mode, where a plan is exactly what it is for.
func (TodoWrite) ReadOnly() bool { return true }

func (TodoWrite) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: TodoToolName,
		Description: "Keep a task list for the current work, shown to the user as a checklist. Use it when work has distinct phases or multiple independent deliverables that need progress tracking; skip it for a single bug fix, a question, or one deliverable with several requirements. " +
			"Each call replaces the whole list, so send every item each time. Mark an item in_progress before starting it, with exactly one " +
			"in_progress at a time, and completed as soon as it is done (not in batches). Only mark completed what is fully done: if tests fail " +
			"or the work is blocked, keep it in_progress and add an item for what is needed. Remove items that no longer apply.",
		Schema: schema(`{"type":"object","properties":{
			"todos":{"type":"array","description":"The complete, updated list","items":{"type":"object","properties":{
				"content":{"type":"string","description":"What to do, in the imperative (\"Run the tests\")"},
				"status":{"type":"string","enum":["pending","in_progress","completed"]},
				"active_form":{"type":"string","description":"The same as an ongoing action (\"Running the tests\"), shown while in progress"}},
				"required":["content","status"]}}},
			"required":["todos"]}`),
	}
}

func (TodoWrite) Run(_ context.Context, _ *Env, input json.RawMessage) Result {
	todos, err := ParseTodos(input)
	if err != nil {
		return errorf("%v", err)
	}
	if len(todos) == 0 {
		return Result{Content: "Task list cleared."}
	}
	done, now := 0, ""
	for _, t := range todos {
		switch t.Status {
		case TodoCompleted:
			done++
		case TodoInProgress:
			now = t.Content
		}
	}
	msg := fmt.Sprintf("Task list updated (%d of %d done).", done, len(todos))
	switch {
	case done == len(todos):
		msg += " Everything is done."
	case now != "":
		msg += " In progress: " + now
	}
	// The model just sent the list; echoing it back would only cost tokens.
	return Result{Content: msg, Display: FormatTodos(todos)}
}

// ParseTodos reads and checks a todo_write input.
func ParseTodos(input json.RawMessage) ([]Todo, error) {
	in, err := decode[struct {
		Todos []Todo `json:"todos"`
	}](input)
	if err != nil {
		return nil, err
	}
	if len(in.Todos) > maxTodos {
		return nil, fmt.Errorf("at most %d items; group smaller steps together", maxTodos)
	}
	active := 0
	for i := range in.Todos {
		t := &in.Todos[i]
		t.Content, t.ActiveForm = strings.TrimSpace(t.Content), strings.TrimSpace(t.ActiveForm)
		if t.Content == "" {
			return nil, fmt.Errorf("item %d has no content", i+1)
		}
		switch t.Status {
		case TodoInProgress:
			active++
		case TodoPending, TodoCompleted:
		default:
			return nil, fmt.Errorf("item %d: status must be pending, in_progress or completed, not %q", i+1, t.Status)
		}
	}
	if active > 1 {
		return nil, fmt.Errorf("%d items are in_progress; keep exactly one in progress at a time", active)
	}
	return in.Todos, nil
}

// FormatTodos renders a list as plain text, one "[x] item" per line.
func FormatTodos(todos []Todo) string {
	var b strings.Builder
	for _, t := range todos {
		mark := "[ ]"
		switch t.Status {
		case TodoInProgress:
			mark = "[>]"
		case TodoCompleted:
			mark = "[x]"
		}
		fmt.Fprintf(&b, "%s %s\n", mark, t.Content)
	}
	return strings.TrimRight(b.String(), "\n")
}

// LatestTodos finds the current task list in a conversation: the input of
// the last todo_write call whose result wasn't an error. ok is false when
// there has been none.
func LatestTodos(msgs []llm.Message) (todos []Todo, ok bool) {
	failed := map[string]bool{}
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult && b.IsError {
				failed[b.ID] = true
			}
		}
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		uses := msgs[i].ToolUses()
		for j := len(uses) - 1; j >= 0; j-- {
			u := uses[j]
			if u.Name != TodoToolName || failed[u.ID] {
				continue
			}
			if t, err := ParseTodos(u.Input); err == nil {
				return t, true
			}
		}
	}
	return nil, false
}
