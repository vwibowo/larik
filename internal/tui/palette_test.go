package tui

import (
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/agent"
	"larik/internal/checkpoint"
	"larik/internal/config"
	"larik/internal/llm/openaicompat"
	"larik/internal/permission"
	"larik/internal/session"
	"larik/internal/tools"
)

// testModel is a TUI model around an agent that is never run.
func testModel(t *testing.T) *model {
	t.Helper()
	dir := t.TempDir()
	sess, err := session.Create(filepath.Join(dir, "sessions"), session.Meta{Cwd: dir, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	a := agent.New(agent.Options{
		Provider:    openaicompat.New("ollama", "", "http://127.0.0.1:1/v1"),
		Model:       "m",
		Cwd:         dir,
		Tools:       tools.Default(),
		Perms:       permission.NewChecker(permission.ModeDefault, permission.Rules{}, dir),
		Session:     sess,
		Checkpoints: checkpoint.New(filepath.Join(dir, "ckpt")),
	})
	cfg := &config.Config{Cwd: dir, ConfigDir: filepath.Join(dir, "config"), Providers: map[string]config.ProviderConfig{}}
	return newModel(Options{Agent: a, Config: cfg})
}

func typeText(m *model, s string) {
	for _, r := range s {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func paletteLabels(m *model) []string {
	var out []string
	for _, it := range m.palette.visible() {
		out = append(out, it.label)
	}
	return out
}

func TestPaletteFiltersAndRanksExactFirst(t *testing.T) {
	m := testModel(t)
	typeText(m, "/mo")
	if got := paletteLabels(m); len(got) != 2 || got[0] != "/model" || got[1] != "/mode" {
		t.Fatalf("/mo: %v", got)
	}
	typeText(m, "de")
	if got := paletteLabels(m); got[0] != "/mode" {
		t.Fatalf("an exact match should lead: %v", got)
	}
	m.Update(press(tea.KeyTab))
	if m.input.Value() != "/mode " || m.palette != nil {
		t.Fatalf("tab should complete and close: %q %v", m.input.Value(), m.palette != nil)
	}
}

func TestPaletteEnterRunsOrCompletes(t *testing.T) {
	m := testModel(t)
	typeText(m, "/res")
	m.Update(press(tea.KeyEnter))
	if m.input.Value() != "/resume " {
		t.Fatalf("a command with a required argument should complete, got %q", m.input.Value())
	}

	m.input.Reset()
	m.syncPalette()
	typeText(m, "/mod")
	m.Update(press(tea.KeyDown)) // /model → /mode
	m.Update(press(tea.KeyEnter))
	if m.modePick == nil || m.input.Value() != "" {
		t.Fatalf("enter on /mode should run it and open the mode picker")
	}
	m.Update(typedKey('3'))
	if m.modePick != nil || m.agent.Perms().Mode() != permission.ModePlan {
		t.Fatalf("3 should pick plan mode, got %s", m.agent.Perms().Mode())
	}
}

func TestPaletteEscHidesUntilInputChanges(t *testing.T) {
	m := testModel(t)
	typeText(m, "/c")
	m.Update(press(tea.KeyEscape))
	if m.palette != nil || m.input.Value() != "/c" {
		t.Fatal("esc should close the palette and keep the input")
	}
	typeText(m, "o")
	if m.palette == nil {
		t.Fatal("typing again should reopen the palette")
	}
	m.input.SetValue("/usr/bin/env foo")
	m.syncPalette()
	if m.palette != nil {
		t.Fatal("input with a space is not a command being typed")
	}
}

func TestModePickerListsYolo(t *testing.T) {
	m := testModel(t)
	m.openModePicker()
	if n := len(m.modePick.items); n != 4 {
		t.Fatalf("want all 4 modes, got %d", n)
	}
	if it, _ := m.modePick.selected(); it.value != permission.ModeDefault {
		t.Fatalf("cursor should start on the current mode, got %v", it.value)
	}
	m.Update(press(tea.KeyEscape))
	if m.modePick != nil || m.agent.Perms().Mode() != permission.ModeDefault {
		t.Fatal("esc should close without changing the mode")
	}
}

func typedKey(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }
