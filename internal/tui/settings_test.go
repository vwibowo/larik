package tui

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/permission"
)

func TestThemeSettingOverridesTerminal(t *testing.T) {
	m := testModel(t)
	m.Update(tea.BackgroundColorMsg{}) // black: a dark terminal
	if !m.isDark {
		t.Fatal("auto should follow a dark terminal")
	}
	m.command("/theme light")
	if m.isDark || m.opts.Config.Theme != "light" {
		t.Fatalf("/theme light should switch now: dark=%v theme=%q", m.isDark, m.opts.Config.Theme)
	}
	m.Update(tea.BackgroundColorMsg{})
	if m.isDark {
		t.Fatal("a fixed theme should ignore the terminal's background")
	}
	data, err := os.ReadFile(m.opts.Config.UserConfigPath())
	if err != nil || !strings.Contains(string(data), `"theme": "light"`) {
		t.Fatalf("theme should be saved to the user config: %s %v", data, err)
	}
	m.command("/theme auto")
	if !m.isDark || m.opts.Config.Theme != "" {
		t.Fatal("auto should go back to the terminal's background")
	}
}

// selectSetting puts the /config cursor on a setting.
func selectSetting(t *testing.T, m *model, key string) {
	t.Helper()
	m.settings.list.selectWhere(func(it pickItem) bool { return it.value == key })
	if it, _ := m.settings.list.selected(); it.value != key {
		t.Fatalf("no %s row", key)
	}
}

func savedConfig(t *testing.T, m *model) string {
	t.Helper()
	data, _ := os.ReadFile(m.opts.Config.UserConfigPath())
	return string(data)
}

func TestConfigPanelPreviewsAndSaves(t *testing.T) {
	m := testModel(t)
	m.command("/config")
	if m.settings == nil {
		t.Fatal("/config should open the settings panel")
	}
	m.Update(press(tea.KeyEnter)) // theme is first
	if m.settings.editing != "theme" {
		t.Fatalf("enter should edit the theme, editing %q", m.settings.editing)
	}
	m.Update(press(tea.KeyDown)) // auto → dark
	m.Update(press(tea.KeyDown)) // dark → light
	if m.isDark {
		t.Fatal("moving onto light should preview it")
	}
	m.Update(press(tea.KeyEscape))
	if !m.isDark || m.opts.Config.Theme != "" {
		t.Fatalf("esc should undo the preview, theme %q", m.opts.Config.Theme)
	}

	selectSetting(t, m, "mode")
	m.Update(press(tea.KeyEnter))
	m.Update(press(tea.KeyDown)) // default → accept edits
	m.Update(press(tea.KeyEnter))
	if m.agent.Perms().Mode() != permission.ModeAcceptEdits || m.opts.Config.Mode != permission.ModeAcceptEdits {
		t.Fatalf("mode should apply and be remembered: %s %s", m.agent.Perms().Mode(), m.opts.Config.Mode)
	}
	if m.settings.values != nil || m.settings.failed {
		t.Fatalf("choosing should go back to the list: %q", m.settings.status)
	}

	selectSetting(t, m, "effort")
	m.Update(press(tea.KeyEnter))
	m.Update(press(tea.KeyDown)) // default → low
	m.Update(press(tea.KeyEnter))
	if m.agent.Effort() != llm.EffortLow {
		t.Fatalf("effort should apply now, got %q", m.agent.Effort())
	}
	if data := savedConfig(t, m); !strings.Contains(data, `"mode": "accept-edits"`) || !strings.Contains(data, `"effort": "low"`) {
		t.Fatalf("settings should be saved: %s", data)
	}
	m.Update(press(tea.KeyEscape))
	if m.settings != nil {
		t.Fatal("esc should close the panel")
	}
}

func TestConfigPanelToggleAndText(t *testing.T) {
	m := testModel(t)
	m.command("/config")
	selectSetting(t, m, "verbose")
	m.Update(press(tea.KeySpace))
	if !m.verbose || !m.showThinking || m.settings.values != nil {
		t.Fatal("space on a toggle should flip it at once")
	}
	if !strings.Contains(savedConfig(t, m), `"verbose": true`) {
		t.Fatalf("verbose should be saved: %s", savedConfig(t, m))
	}

	selectSetting(t, m, "language")
	m.Update(press(tea.KeyEnter))
	if m.settings.input == nil {
		t.Fatal("enter on language should open a text field")
	}
	typeText(m, "Indonesian")
	m.Update(press(tea.KeyEnter))
	if m.opts.Config.Language != "Indonesian" || m.settings.input != nil {
		t.Fatalf("enter should save the language, got %q", m.opts.Config.Language)
	}
	if !strings.Contains(m.settings.status, "/clear") {
		t.Fatalf("the status should say when a language change applies: %q", m.settings.status)
	}
}

func TestConfigKeyValue(t *testing.T) {
	m := testModel(t)
	m.command("/config auto_compact=false")
	if c := m.opts.Config; c.AutoCompactOn() || !strings.Contains(savedConfig(t, m), `"auto_compact": false`) {
		t.Fatalf("auto_compact=false should apply and save: %s", savedConfig(t, m))
	}
	m.command("/config notifications=loud")
	if m.notify != "off" {
		t.Fatal("an invalid value should change nothing")
	}
	m.command("/config notifications desktop")
	if m.notify != "desktop" {
		t.Fatalf("key value with a space should work too, got %q", m.notify)
	}
	m.command("/config notifications=off")
	if strings.Contains(savedConfig(t, m), "notifications") {
		t.Fatal("the default value should remove the key")
	}
}

func TestUndoHistorySetting(t *testing.T) {
	m := testModel(t)
	spec, _ := settingByKey("checkpoint_retention_days")
	if spec.get(m) != "7" {
		t.Fatalf("default = %q", spec.get(m))
	}
	m.command("/config checkpoint_retention_days=14")
	if m.opts.Config.CheckpointRetentionDays != 14 || !strings.Contains(savedConfig(t, m), `"checkpoint_retention_days": 14`) {
		t.Fatalf("any number of days should apply and save: %s", savedConfig(t, m))
	}
	if spec.label(spec.get(m)) != "14 days" {
		t.Fatalf("label = %q", spec.label(spec.get(m)))
	}
	m.command("/config checkpoint_retention_days=soon")
	if m.opts.Config.CheckpointRetentionDays != 14 {
		t.Fatal("an invalid value should change nothing")
	}
	m.command("/config checkpoint_retention_days=forever")
	if m.opts.Config.CheckpointRetention() != 0 || !strings.Contains(savedConfig(t, m), `"checkpoint_retention_days": -1`) {
		t.Fatalf("forever should keep snapshots: %s", savedConfig(t, m))
	}
	m.command("/config checkpoint_retention_days=7")
	if strings.Contains(savedConfig(t, m), "checkpoint_retention_days") {
		t.Fatal("the default should remove the key")
	}
}

func TestVerboseShowsMoreToolOutput(t *testing.T) {
	m := testModel(t)
	out := strings.Repeat("line\n", 30)
	ev := agent.Event{Kind: agent.EvToolEnd, ToolName: "bash", Input: []byte(`{"command":"seq 30"}`), Output: out}
	short := strings.Count(plain(m.renderToolCard(ev)), "line")
	m.verbose = true
	long := strings.Count(plain(m.renderToolCard(ev)), "line")
	if short >= 10 || long < 30 {
		t.Fatalf("verbose should show the full output: %d lines normally, %d verbose", short, long)
	}
}

func TestNotifyOnlyWhenUnfocused(t *testing.T) {
	m := testModel(t)
	m.notify = "bell"
	perm := agent.Event{Kind: agent.EvPermission, ToolName: "bash"}
	if cmd := m.handleEvent(perm); cmd != nil {
		t.Fatal("no notification while the terminal has focus")
	}
	m.perm = nil
	m.Update(tea.BlurMsg{})
	cmd := m.handleEvent(perm)
	if cmd == nil {
		t.Fatal("a permission prompt in an unfocused terminal should notify")
	}
	if raw, ok := cmd().(tea.RawMsg); !ok || raw.Msg != "\a" {
		t.Fatalf("bell should ring: %#v", cmd())
	}
}

func TestSpinnerTip(t *testing.T) {
	m := testModel(t)
	m.height, m.running, m.tip = 40, true, "a tip"
	if !strings.Contains(plain(m.liveView()), "Tip: a tip") {
		t.Fatal("the tip should show under the spinner")
	}
	m.tips = false
	if strings.Contains(plain(m.liveView()), "Tip:") {
		t.Fatal("spinner_tips off should hide it")
	}
}
