package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestKeybindingsEditorSavesAndApplies(t *testing.T) {
	m := testModel(t)
	m.command("/keybindings")
	if m.settings == nil || m.settings.keybindings == nil {
		t.Fatal("/keybindings should open the native editor")
	}
	m.Update(press(tea.KeyEnter)) // submit
	m.settings.keybindings.input.SetValue("ctrl+enter, alt+s")
	m.Update(press(tea.KeyEnter))
	m.Update(typedKey('s'))
	if !m.keys.is("ctrl+enter", actSubmit) || !m.keys.is("alt+s", actSubmit) {
		t.Fatalf("saved keys were not applied: %v", m.keys.keys[actSubmit])
	}
	if !strings.Contains(savedConfig(t, m), `"ctrl+enter"`) {
		t.Fatalf("keybindings were not saved: %s", savedConfig(t, m))
	}
}

func TestKeybindingsEditorUnbindRestoreAndValidation(t *testing.T) {
	m := testModel(t)
	m.command("/config keybindings")
	if m.settings == nil || m.settings.keybindings == nil {
		t.Fatal("/config keybindings should open the native editor")
	}
	m.Update(typedKey('u')) // submit
	if keys, ok := m.settings.keybindings.bindings[actSubmit]; !ok || len(keys) != 0 {
		t.Fatal("u should unbind the selected action in the draft")
	}
	m.Update(typedKey('r'))
	if _, ok := m.settings.keybindings.bindings[actSubmit]; ok {
		t.Fatal("r should restore the selected action's defaults")
	}
	m.Update(press(tea.KeyEnter))
	m.settings.keybindings.input.SetValue("ctrl+c")
	m.Update(press(tea.KeyEnter))
	if m.settings.keybindings.input == nil || !strings.Contains(m.settings.keybindings.status, "can't be rebound") {
		t.Fatalf("ctrl+c should be rejected: %q", m.settings.keybindings.status)
	}
}
