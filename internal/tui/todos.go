package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"larik/internal/agent"
	"larik/internal/tools"
)

// The model's task list (todo_write) lives in the transcript. The TUI keeps
// the latest state for the floating task card and archives its completed list.

// updateTodos records the list from a finished top-level todo_write call.
func (m *model) updateTodos(e agent.Event) bool {
	if e.ToolName != tools.TodoToolName || e.IsError || e.Agent != "" {
		return false
	}
	todos, err := tools.ParseTodos(e.Input)
	if err != nil {
		return false
	}
	m.todos = todos
	if len(todos) == 0 || m.openTodos() {
		m.todoCompletionPrinted = false
		return false
	}
	if m.todoCompletionPrinted {
		return false
	}
	m.todoCompletionPrinted = true
	return true
}

// openTodos reports whether the list has anything left to do.
func (m *model) openTodos() bool {
	for _, t := range m.todos {
		if t.Status != tools.TodoCompleted {
			return true
		}
	}
	return false
}

// activeTodo is the item in progress, as an ongoing action if it has one.
func (m *model) activeTodo() string {
	for _, t := range m.todos {
		if t.Status == tools.TodoInProgress {
			if t.ActiveForm != "" {
				return t.ActiveForm
			}
			return t.Content
		}
	}
	return ""
}

func todoProgress(todos []tools.Todo) string {
	done := 0
	for _, t := range todos {
		if t.Status == tools.TodoCompleted {
			done++
		}
	}
	return fmt.Sprintf("%d/%d done", done, len(todos))
}

// todoLines renders a list as a checklist, at most limit rows (0: all).
// A long list keeps the item in progress in view, with the rows before
// and after it summarized.
func (m *model) todoLines(todos []tools.Todo, limit int) []string {
	return m.todoLinesWidth(todos, limit, max(m.width-8, 10))
}

func (m *model) todoLinesWidth(todos []tools.Todo, limit, width int) []string {
	first, last := 0, len(todos)
	if limit > 0 && len(todos) > limit {
		cur := 0
		for i, t := range todos {
			if t.Status == tools.TodoInProgress {
				cur = i
				break
			}
			if t.Status == tools.TodoCompleted {
				cur = i + 1 // past the finished ones, when nothing is in progress
			}
		}
		first = max(min(cur-1, len(todos)-limit), 0)
		last = first + limit
	}
	var out []string
	if first > 0 {
		out = append(out, m.st.dim.Render(fmt.Sprintf("… %d earlier", first)))
	}
	for _, t := range todos[first:last] {
		text := ansi.Truncate(t.Content, width, "…")
		switch t.Status {
		case tools.TodoCompleted:
			out = append(out, m.st.ok.Render("✔ ")+m.st.dim.Strikethrough(true).Render(text))
		case tools.TodoInProgress:
			out = append(out, m.st.accent.Render("◼ "+text))
		default:
			out = append(out, m.st.dim.Render("◻ ")+text)
		}
	}
	if last < len(todos) {
		out = append(out, m.st.dim.Render(fmt.Sprintf("… %d more", len(todos)-last)))
	}
	return out
}

// liveTodos is the pinned checklist shown while a turn runs, if the list
// has open items.
func (m *model) liveTodos() []string {
	if !m.openTodos() {
		return nil
	}
	out := []string{m.st.dim.Render("Tasks · " + todoProgress(m.todos))}
	for _, l := range m.todoLines(m.todos, min(max(m.height/4, 3), 5)) {
		out = append(out, "  "+l)
	}
	return out
}

// completedTodosCard archives the final checklist in the conversation once.
func (m *model) completedTodosCard() string {
	return m.st.ok.Render("✔ Tasks complete · "+todoProgress(m.todos)) + "\n" + strings.Join(m.todoLines(m.todos, 0), "\n")
}

// todosCommand handles /todos: the current list, in full.
func (m *model) todosCommand() string {
	if len(m.todos) == 0 {
		return m.st.dim.Render("no task list yet (the model keeps one with todo_write for multi-step work)")
	}
	return m.st.dim.Render("Tasks · "+todoProgress(m.todos)) + "\n" + prefixLines(strings.Join(m.todoLines(m.todos, 0), "\n"), "  ")
}
