package tui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/config"
	"larik/internal/permission"
)

func TestPermissionsEditorSavesAndAppliesUserRule(t *testing.T) {
	m := testModel(t)
	m.command("/permissions")
	if m.settings == nil || m.settings.rules == nil {
		t.Fatal("/permissions should open the native rule editor")
	}
	m.Update(press(tea.KeyEnter)) // + Add allow rule
	typeText(m, "bash(go test*)")
	m.Update(press(tea.KeyEnter))
	if !m.settings.rules.dirty[0] || m.settings.rules.input != nil {
		t.Fatal("valid rule should update the draft")
	}
	m.Update(typedKey('s'))
	if m.settings.rules.dirty[0] || !strings.Contains(savedConfig(t, m), `"bash(go test*)"`) {
		t.Fatalf("rule was not saved: %s", savedConfig(t, m))
	}
	decision, _ := m.agent.Perms().Decide(permission.Call{Tool: "bash", Input: json.RawMessage(`{"command":"go test ./...","sandbox":false}`)})
	if decision != permission.Allow {
		t.Fatalf("saved rule should apply immediately, got %v", decision)
	}
}

func TestPermissionsEditorValidatesAndSavesPrivateProjectRule(t *testing.T) {
	m := testModel(t)
	m.command("/config permissions")
	if m.settings == nil || m.settings.rules == nil {
		t.Fatal("/config permissions should open the same native editor")
	}
	m.Update(press(tea.KeyTab))
	if m.settings.rules.scope != 1 {
		t.Fatal("tab should switch to private project rules")
	}
	m.Update(press(tea.KeyDown))
	m.Update(press(tea.KeyEnter)) // + Add deny rule
	typeText(m, "bash(")
	m.Update(press(tea.KeyEnter))
	if m.settings.rules.input == nil || !m.settings.rules.failed {
		t.Fatal("invalid rule should remain in the input with an error")
	}
	m.settings.rules.input.SetValue("bash(rm*)")
	m.Update(press(tea.KeyEnter))
	m.Update(typedKey('s'))
	data, err := os.ReadFile(config.LocalSettingsPath(m.opts.Config.Cwd))
	if err != nil || !strings.Contains(string(data), `"bash(rm*)"`) {
		t.Fatalf("private project deny was not saved: %s %v", data, err)
	}
	decision, _ := m.agent.Perms().Decide(permission.Call{Tool: "bash", Input: json.RawMessage(`{"command":"rm file","sandbox":false}`)})
	if decision != permission.Deny {
		t.Fatalf("saved deny should apply immediately, got %v", decision)
	}
}

func TestPermissionsEditorWarnsBeforeDiscardingDraft(t *testing.T) {
	m := testModel(t)
	m.command("/permissions")
	m.Update(press(tea.KeyEnter))
	typeText(m, "read(.env)")
	m.Update(press(tea.KeyEnter))
	m.Update(press(tea.KeyEscape))
	if m.settings == nil || !m.settings.rules.closing {
		t.Fatal("first escape should warn before discarding an unsaved draft")
	}
	m.Update(press(tea.KeyEscape))
	if m.settings != nil {
		t.Fatal("second escape should discard and close")
	}
}
