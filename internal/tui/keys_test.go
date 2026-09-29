package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/config"
)

func TestKeymapDefaults(t *testing.T) {
	km, warn := newKeymap(nil)
	if len(warn) != 0 {
		t.Fatalf("warnings: %v", warn)
	}
	for k, want := range map[string]string{"enter": actSubmit, "ctrl+j": actNewline, "ctrl+o": actToggleThinking, "?": actShortcuts, "pgup": actScrollUp} {
		if got := km.action[k]; got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestKeymapRebinds(t *testing.T) {
	km, warn := newKeymap(map[string]config.KeyList{
		actToggleThinking: {"ctrl+g", "ctrl+g"}, // taken from external_editor
		actPasteImage:     {"Alt+V", "ctrl+v"},
		actModelPicker:    {}, // unbound
		"bogus":           {"ctrl+b"},
		actSubmit:         {"ctrl+c"}, // reserved
		actHistorySearch:  {"x", "hyper+ctrl+r", "ctrl+nope"},
	})
	if km.action["ctrl+g"] != actToggleThinking || km.action["ctrl+o"] != "" {
		t.Errorf("toggle_thinking = %v", km.keys[actToggleThinking])
	}
	if got := km.keys[actExternalEditor]; len(got) != 0 {
		t.Errorf("external_editor keeps %v after losing its key", got)
	}
	if got := km.keys[actPasteImage]; strings.Join(got, " ") != "alt+v ctrl+v" {
		t.Errorf("paste_image = %v", got)
	}
	if km.hint(actModelPicker) != "" || km.all(actModelPicker) != "unbound" || km.binding(actModelPicker).Enabled() {
		t.Error("model_picker should be unbound")
	}
	if got := km.keys[actSubmit]; len(got) != 0 {
		t.Errorf("submit = %v; its only key was invalid, so the setting leaves it unbound", got)
	}
	if got := km.keys[actHistorySearch]; strings.Join(got, " ") != "hyper+ctrl+r" {
		t.Errorf("history_search = %v", got)
	}
	all := strings.Join(warn, "\n")
	for _, want := range []string{`unknown action "bogus"`, "ctrl+c can't be rebound", `"x": a character without a modifier`, "ctrl+g is already bound to toggle_thinking", `unknown key "nope"`} {
		if !strings.Contains(all, want) {
			t.Errorf("warnings lack %q:\n%s", want, all)
		}
	}
}

func TestReboundKeysDriveTheComposer(t *testing.T) {
	m := testModel(t)
	m.opts.Config.Keybindings = map[string]config.KeyList{actToggleThinking: {"ctrl+t"}, actCycleMode: {}}
	m = newModel(m.opts)
	m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if m.showThinking {
		t.Fatal("ctrl+o still toggles thinking after the rebind")
	}
	m.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if !m.showThinking {
		t.Fatal("ctrl+t should toggle thinking")
	}
	if line := plain(m.statusLine()); strings.Contains(line, "shift+tab") {
		t.Errorf("the footer hints at an unbound key: %q", line)
	}
	if !strings.Contains(plain(m.shortcutsView()), "ctrl+t") {
		t.Error("the shortcuts overlay should show the new key")
	}
	if got := nextTip(m.keys); strings.HasPrefix(got, "shift+tab") || strings.HasPrefix(got, "ctrl+o") {
		t.Errorf("tip names an old key: %q", got)
	}
}
