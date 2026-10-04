package tui

import (
	"strings"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/llm/openaicompat"
	"larik/internal/permission"
	"larik/internal/session"
	"larik/internal/tools"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func savedMessagesModel(t *testing.T) *model {
	t.Helper()
	m := testModel(t)
	dir := m.opts.Config.Cwd
	s, err := session.Create(filepath.Join(dir, "messages"), session.Meta{Cwd: dir, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(llm.UserText("first prompt"), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{llm.TextBlock("first response")}}, nil); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	s.Close()
	var st *session.State
	s, st, err = session.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	m.agent = agent.New(agent.Options{
		Provider: openaicompat.New("ollama", "", "http://127.0.0.1:1/v1"),
		Model:    "m", Cwd: dir, Tools: tools.Default(),
		Perms: permission.NewChecker(permission.ModeDefault, permission.Rules{}, dir), Session: s,
	})
	m.agent.Restore(st)
	return m
}

func TestReloadPromptIsVisible(t *testing.T) {
	m := testModel(t)
	m.askReload("Apply reload?", func() tea.Cmd { return nil })
	kind, panel := m.panel()
	if kind != "reload" || !strings.Contains(plain(panel), "Apply reload?") || !m.hasPickerPanel() {
		t.Fatalf("reload confirmation should be the active visible panel: kind=%q panel=%q", kind, plain(panel))
	}
}

func TestLanguageChangeConfirmsBeforeSavingAndClearing(t *testing.T) {
	m := savedMessagesModel(t)
	m.appendOutput("visible conversation")
	m.command("/config language=Indonesian")
	if m.reload == nil || m.opts.Config.Language != "" {
		t.Fatal("language must not save before confirmation")
	}
	m.Update(press(tea.KeyEscape))
	if m.reload != nil || m.opts.Config.Language != "" || len(m.outputs) == 0 {
		t.Fatal("cancel should preserve settings and conversation")
	}
	m.command("/config language=Indonesian")
	m.Update(press(tea.KeyEnter))
	if m.reload != nil || m.opts.Config.Language != "Indonesian" || len(m.outputs) != 0 {
		t.Fatal("confirm should save the language and clear model context/view")
	}
}

func TestLanguageChangeAfterClearDoesNotConfirmAgain(t *testing.T) {
	m := savedMessagesModel(t)
	m.agent.Clear()
	m.command("/config language=Indonesian")
	if m.reload != nil || m.opts.Config.Language != "Indonesian" {
		t.Fatal("an already-empty model context should apply language without another confirmation")
	}
}

func TestExecutionChangeConfirmsBeforeSaving(t *testing.T) {
	m := savedMessagesModel(t)
	m.command("/execution code")
	if m.reload == nil || m.agent.Execution() == "code" {
		t.Fatal("execution must wait for confirmation")
	}
	m.Update(press(tea.KeyEscape))
	if m.reload != nil || m.agent.Execution() == "code" {
		t.Fatal("cancel should not change execution")
	}
	m.command("/execution code")
	m.Update(press(tea.KeyEnter))
	if m.agent.Execution() != "code" {
		t.Fatalf("confirmed execution change not active: %s", m.agent.Execution())
	}
}
