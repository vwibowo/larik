package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/llm"
	"larik/internal/session"
)

func TestSessionPickerFiltersAndScrolls(t *testing.T) {
	m := testModel(t)
	dir := filepath.Join(m.opts.Config.Cwd, "sessions")
	m.opts.SessionDir = dir
	var target string
	for i := range 18 {
		s, err := session.Create(dir, session.Meta{Cwd: m.opts.Config.Cwd, Model: "m"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			target = s.ID
			if err := s.AppendMessage(llm.UserText("special prompt"), nil); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	m.height = 20
	m.command("/sessions")
	if m.sessionPick == nil || len(m.sessionPick.items) != 19 {
		t.Fatalf("expected all sessions in picker, got %v", m.sessionPick)
	}
	if !strings.Contains(plain(m.View().Content), "↓ more") {
		t.Fatal("session picker should scroll instead of printing every session")
	}
	typeText(m, "special prompt")
	items := m.sessionPick.visible()
	if len(items) != 1 || items[0].value != target {
		t.Fatalf("title filter selected %v, want %s", items, target)
	}
	m.Update(press(tea.KeyEscape))
	if m.sessionPick != nil {
		t.Fatal("escape should close session picker")
	}
	m.command("/resume")
	if m.sessionPick == nil {
		t.Fatal("/resume without ID should open session picker")
	}
}

// /rename names the session beside its transcript, and /sessions shows
// and filters by the name, its first prompt still there.
func TestRenameSession(t *testing.T) {
	m := testModel(t)
	path := m.agent.SessionPath()
	m.opts.SessionDir = filepath.Dir(path)
	m.command("/rename   Parser   work ")
	if got, _ := os.ReadFile(session.NamePath(path)); string(got) != "Parser work\n" {
		t.Fatalf("name file: %q", got)
	}
	m.command("/sessions")
	if m.sessionPick == nil || len(m.sessionPick.items) != 1 || !strings.Contains(m.sessionPick.items[0].detail, "Parser work") {
		t.Fatalf("picker: %+v", m.sessionPick)
	}
	typeText(m, "parser")
	if len(m.sessionPick.visible()) != 1 {
		t.Fatal("the name doesn't filter")
	}
	m.Update(press(tea.KeyEscape))
	m.command("/rename")
	if _, err := os.Stat(session.NamePath(path)); err == nil {
		t.Fatal("/rename with no name kept the name")
	}
}
