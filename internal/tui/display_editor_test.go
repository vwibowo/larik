package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestStatusLineEditorSavesAppliesAndPreviews(t *testing.T) {
	m := testModel(t)
	m.command("/statusline")
	if m.settings == nil || m.settings.display == nil {
		t.Fatal("/statusline should open the command editor")
	}
	m.Update(press(tea.KeyEnter)) // Command
	typeText(m, "printf preview")
	m.Update(press(tea.KeyEnter))
	m.Update(press(tea.KeyDown))
	m.Update(press(tea.KeyEnter)) // Refresh interval
	typeText(m, "500")
	m.Update(press(tea.KeyEnter))

	cmd := m.handleDisplayKey(typedKey('t'))
	if cmd == nil {
		t.Fatal("preview should run the draft command")
	}
	m.Update(cmd())
	if got := strings.Join(m.settings.display.preview, "\n"); got != "preview" {
		t.Fatalf("preview = %q (%s)", got, m.settings.display.status)
	}
	m.Update(typedKey('s'))
	if m.status == nil || m.status.command != "printf preview" || m.status.interval.Milliseconds() != 500 {
		t.Fatalf("saved status command was not applied: %+v", m.status)
	}
	saved := savedConfig(t, m)
	if !strings.Contains(saved, `"status_line"`) || !strings.Contains(saved, `"refresh_interval_ms": 500`) {
		t.Fatalf("status line was not saved: %s", saved)
	}
}

func TestDisplayEditorValidatesIntervalAndDisables(t *testing.T) {
	m := testModel(t)
	m.command("/sidebar-config")
	m.Update(press(tea.KeyDown))
	m.Update(press(tea.KeyEnter))
	typeText(m, "100")
	m.Update(press(tea.KeyEnter))
	if m.settings.display.input == nil || !m.settings.display.failed {
		t.Fatal("an interval below 300 ms should be rejected")
	}
	m.Update(press(tea.KeyEscape))

	m.settings.display.command = "echo old"
	m.settings.display.rebuild()
	m.Update(press(tea.KeyDown))
	m.Update(press(tea.KeyDown))
	m.Update(press(tea.KeyDown))
	m.Update(press(tea.KeyEnter)) // Disable
	m.Update(typedKey('s'))
	if m.sidebarStatus != nil || m.opts.Config.Sidebar != nil || strings.Contains(savedConfig(t, m), `"sidebar"`) {
		t.Fatalf("disable should remove and stop the sidebar: %s", savedConfig(t, m))
	}
}

func TestConfigRowsOpenDisplayEditors(t *testing.T) {
	m := testModel(t)
	m.command("/config status_line")
	if m.settings == nil || m.settings.display == nil || m.settings.display.key != "status_line" {
		t.Fatal("/config status_line should open the native editor")
	}
}
