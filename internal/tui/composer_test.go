package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// withFiles creates files under the model's working directory and indexes
// them, as the first @ would.
func withFiles(t *testing.T, m *model, names ...string) {
	t.Helper()
	for _, n := range names {
		p := filepath.Join(m.agent.Cwd(), n)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m.Update(indexFiles(m.agent.Cwd())())
}

func TestMentionPopupCompletesAndDrillsDown(t *testing.T) {
	m := testModel(t)
	withFiles(t, m, "internal/tui/app.go", "internal/tui/view.go", "main.go", "README.md")
	typeText(m, "fix @inter")
	if m.mention == nil || len(m.mention.items) == 0 || m.mention.items[0].value != "internal/" {
		t.Fatalf("popup should offer internal/ first: %+v", m.mention)
	}
	m.Update(press(tea.KeyTab))
	if got := m.input.Value(); got != "fix @internal/" || m.mention == nil {
		t.Fatalf("a directory should complete and stay open: %q", got)
	}
	if m.mention.items[0].value != "internal/tui/" {
		t.Fatalf("drill-down should list the directory's contents: %+v", m.mention.items)
	}
	m.Update(press(tea.KeyTab))
	typeText(m, "vi")
	m.Update(press(tea.KeyEnter))
	if got := m.input.Value(); got != "fix @internal/tui/view.go " || m.mention != nil {
		t.Fatalf("a file should complete with a space and close: %q", got)
	}
	if m.running {
		t.Fatal("enter in the popup should not send the prompt")
	}
}

func TestMentionPopupRanksNamesAndHidesOnEsc(t *testing.T) {
	m := testModel(t)
	withFiles(t, m, "cmd/larik/main.go", "internal/app/app.go", "docs/main-notes.md")
	typeText(m, "@main")
	var got []string
	for _, it := range m.mention.items {
		got = append(got, it.value.(string))
	}
	if len(got) < 2 || got[0] != "cmd/larik/main.go" || got[1] != "docs/main-notes.md" {
		t.Fatalf("ranking: %v", got)
	}
	m.Update(press(tea.KeyEscape))
	typeText(m, ".g")
	if m.mention != nil {
		t.Fatal("esc should keep the popup closed while the same mention is typed")
	}
	typeText(m, " @")
	if m.mention == nil {
		t.Fatal("a new mention should open the popup again")
	}
}

func TestMentionInsertsMidTextAndOnLaterLines(t *testing.T) {
	m := testModel(t)
	withFiles(t, m, "a b/notes.txt")
	m.input.SetValue("first line\nsee @no")
	m.syncComposer()
	m.Update(press(tea.KeyEnter))
	if got := m.input.Value(); got != "first line\nsee @\"a b/notes.txt\" " {
		t.Fatalf("got %q", got)
	}
	if m.input.Line() != 1 {
		t.Fatalf("cursor should stay on the mention's line, got %d", m.input.Line())
	}
}

func TestHistoryRecallAndDraft(t *testing.T) {
	m := testModel(t)
	m.recordHistory("one")
	m.recordHistory("two")
	m.recordHistory("two")
	if len(m.history) != 2 {
		t.Fatalf("repeats should be skipped: %v", m.history)
	}
	typeText(m, "draft")
	for _, want := range []string{"two", "one", "one"} {
		m.Update(press(tea.KeyUp))
		if got := m.input.Value(); got != want {
			t.Fatalf("up: got %q, want %q", got, want)
		}
	}
	for _, want := range []string{"two", "draft"} {
		m.Update(press(tea.KeyDown))
		if got := m.input.Value(); got != want {
			t.Fatalf("down: got %q, want %q", got, want)
		}
	}
	m.Update(press(tea.KeyUp))
	typeText(m, "!")
	m.Update(press(tea.KeyDown))
	if got := m.input.Value(); got != "two!" {
		t.Fatalf("editing a recalled prompt should leave history browsing: %q", got)
	}
}

func TestHistoryPersistsAndSearches(t *testing.T) {
	m := testModel(t)
	m.opts.Config.DataDir = t.TempDir()
	m.recordHistory("explain the parser")
	m.recordHistory("run the tests")

	again := newModel(Options{Agent: m.agent, Config: m.opts.Config})
	if len(again.history) != 2 || again.history[1] != "run the tests" {
		t.Fatalf("history should load per project: %v", again.history)
	}
	again.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if again.histPick == nil {
		t.Fatal("ctrl+r should open history search")
	}
	typeText(again, "pars")
	again.Update(press(tea.KeyEnter))
	if again.histPick != nil || again.input.Value() != "explain the parser" {
		t.Fatalf("search should fill the composer: %q", again.input.Value())
	}
	if entries, _ := os.ReadDir(filepath.Join(m.opts.Config.DataDir, "sessions")); len(entries) != 0 {
		t.Fatal("history must stay out of the sessions directory")
	}
}

func TestShellModeRunsCommand(t *testing.T) {
	m := testModel(t)
	typeText(m, "!echo hi")
	if m.input.Prompt != "! " {
		t.Fatalf("shell mode prompt: %q", m.input.Prompt)
	}
	m.Update(press(tea.KeyEnter))
	if m.shellCancel == nil || !strings.Contains(m.busyLabel, "echo hi") {
		t.Fatalf("enter should start the command: %q", m.busyLabel)
	}
	if m.input.Prompt != "› " {
		t.Fatal("the prompt should return to normal once the input clears")
	}
	m.Update(shellDoneMsg{command: "echo hi", res: m.agent.Shell(t.Context(), "echo hi")})
	if m.shellCancel != nil || m.busyLabel != "" {
		t.Fatal("finishing should clear the running state")
	}
}

func TestPastedPathBecomesMention(t *testing.T) {
	m := testModel(t)
	withFiles(t, m, "my file.go")
	abs := filepath.Join(m.agent.Cwd(), "my file.go")
	typeText(m, "look at")
	m.Update(tea.PasteMsg{Content: strings.ReplaceAll(abs, " ", `\ `)})
	if got := m.input.Value(); got != `look at @"my file.go" ` {
		t.Fatalf("dropped file: %q", got)
	}
	m.input.Reset()
	m.Update(tea.PasteMsg{Content: "main.go is fine"})
	if got := m.input.Value(); got != "main.go is fine" {
		t.Fatalf("prose should paste as is: %q", got)
	}
}
