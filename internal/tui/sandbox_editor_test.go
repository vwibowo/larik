package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/config"
)

func moveDown(m *model, n int) {
	for range n {
		m.Update(press(tea.KeyDown))
	}
}

func TestSandboxEditorSavesTypedControlsAndLists(t *testing.T) {
	m := testModel(t)
	m.command("/sandbox-config")
	if m.settings == nil || m.settings.sandbox == nil {
		t.Fatal("/sandbox-config should open the native editor")
	}
	m.Update(press(tea.KeyEnter)) // enabled: default -> on
	moveDown(m, 1)
	m.Update(press(tea.KeyEnter)) // network on
	moveDown(m, 2)
	m.Update(press(tea.KeyEnter)) // add writable
	typeText(m, "/tmp/build")
	m.Update(press(tea.KeyEnter))
	moveDown(m, 4)
	m.Update(press(tea.KeyEnter)) // add domain
	typeText(m, "example.com")
	m.Update(press(tea.KeyEnter))
	m.Update(typedKey('s'))

	cfg, err := config.SandboxAt(m.opts.Config.UserConfigPath())
	if err != nil || cfg.Enabled == nil || !*cfg.Enabled || !cfg.Network {
		t.Fatalf("sandbox switches = %+v, %v", cfg, err)
	}
	if len(cfg.Writable) != 1 || cfg.Writable[0] != "/tmp/build" || len(cfg.AllowedDomains) != 1 || cfg.AllowedDomains[0] != "example.com" {
		t.Fatalf("sandbox lists = %+v", cfg)
	}
}

func TestSandboxEditorValidatesAndSupportsPrivateScope(t *testing.T) {
	m := testModel(t)
	m.command("/config sandbox")
	if m.settings == nil || m.settings.sandbox == nil {
		t.Fatal("/config sandbox should open the native editor")
	}
	m.Update(press(tea.KeyTab))
	moveDown(m, 2)
	m.Update(press(tea.KeyEnter))
	typeText(m, "relative/path")
	m.Update(press(tea.KeyEnter))
	if m.settings.sandbox.input == nil || !strings.Contains(m.settings.sandbox.status, "absolute") {
		t.Fatal("relative writable path should be rejected")
	}
	m.settings.sandbox.input.SetValue("~/build")
	m.Update(press(tea.KeyEnter))
	m.Update(typedKey('s'))
	cfg, err := config.SandboxAt(config.LocalSettingsPath(m.opts.Config.Cwd))
	if err != nil || len(cfg.Writable) != 1 || cfg.Writable[0] != "~/build" {
		t.Fatalf("private sandbox = %+v, %v", cfg, err)
	}
}

func TestSandboxEditorWarnsWhenWideningAccess(t *testing.T) {
	m := testModel(t)
	m.command("/sandbox-config")
	moveDown(m, 1)
	m.Update(press(tea.KeyEnter))
	if !strings.Contains(strings.ToLower(m.settings.sandbox.status), "widens") {
		t.Fatalf("network toggle should warn: %q", m.settings.sandbox.status)
	}
}
