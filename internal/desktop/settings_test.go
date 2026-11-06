package desktop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"larik/internal/config"
	"larik/internal/desktop/integration"
	"larik/internal/llm"
	"larik/internal/providers"
)

func settingsApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	return New(Options{Cwd: dir, DataDir: t.TempDir()})
}

func TestSettingsNavigationSearchAndLayouts(t *testing.T) {
	a := settingsApp(t)
	defer a.Close()
	for _, dark := range []bool{false, true} {
		tt := ui.NewTester(a.settingsView, 960, 680)
		tt.SetDark(dark)
		tt.Frame()
		if !tt.HasText("Show sidebar") || !tt.HasText("Search settings") {
			t.Fatalf("window settings missing: %v", tt.Texts())
		}
		if row, ok := tt.Find("Show sidebar"); !ok || row.Y >= toolbarHeight {
			t.Fatalf("settings content starts below an empty header: %+v", row)
		}
		if n := settingsTextCount(tt.Texts(), "Settings"); n != 0 {
			t.Fatalf("sidebar heading remains (%d): %v", n, tt.Texts())
		}
		name := "settings-light.png"
		if dark {
			name = "settings-dark.png"
		}
		snapshot(t, tt, name)
		if err := tt.Click("Search settings"); err != nil {
			t.Fatal(err)
		}
		if !tt.Focused("Search settings") {
			t.Fatal("search did not take keyboard focus")
		}
		tt.Type("MCP")
		tt.Frame()
		if !tt.HasText("Load MCP tools on demand") || !tt.HasText("MCP servers") || tt.HasText("Show sidebar") {
			t.Fatalf("search results: %v", tt.Texts())
		}
		a.settings.query = "nothing-matches-this"
		tt.Frame()
		if !tt.HasText("No settings found") {
			t.Fatalf("empty search: %v", tt.Texts())
		}
		a.settings.query = ""
		if err := tt.Click("Show Terminal settings"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if !tt.HasText("13 pt") {
			t.Fatalf("terminal size: %v", tt.Texts())
		}
		if n := settingsTextCount(tt.Texts(), "Terminal"); n != 2 {
			t.Fatalf("terminal page title remains (%d instances): %v", n, tt.Texts())
		}
		if err := tt.Click("Adjust terminal text size"); err != nil {
			t.Fatal(err)
		}
		tt.Key(0, ui.KeyRight)
		tt.Frame()
		if a.termFont().Size <= 13 {
			t.Fatalf("slider did not increase text size: %g", a.termFont().Size)
		}
		if err := tt.Click("Reset terminal text size"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if a.termFont().Size != 13 {
			t.Fatalf("reset left text at %g pt", a.termFont().Size)
		}
		tt = ui.NewTester(a.settingsView, 760, 500)
		tt.SetDark(dark)
		tt.Frame()
		if !tt.HasText("Terminal text size") {
			t.Fatalf("minimum window: %v", tt.Texts())
		}
		if dark {
			snapshot(t, tt, "settings-minimum-dark.png")
		} else {
			snapshot(t, tt, "settings-minimum-light.png")
		}
		a.settings.page = "window"
	}
}

func settingsTextCount(texts []string, want string) int {
	n := 0
	for _, text := range texts {
		if text == want {
			n++
		}
	}
	return n
}

func TestPaneBackgroundOpacitySetting(t *testing.T) {
	a := settingsApp(t)
	defer a.Close()
	if a.paneOpacity != 1 || a.state().PaneOpacity != nil {
		t.Fatal("existing Desktop state should remain opaque")
	}
	tt := ui.NewTester(a.settingsView, 960, 680)
	if err := tt.Click("Show Appearance settings"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Adjust pane background opacity"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyLeft)
	if a.paneOpacity >= 1 || a.state().PaneOpacity == nil || a.state().TerminalOpacity != nil {
		t.Fatalf("opacity slider did not change saved state: %g", a.paneOpacity)
	}
	if err := tt.Click("Reset pane background opacity"); err != nil {
		t.Fatal(err)
	}
	if a.paneOpacity != 1 || a.state().PaneOpacity != nil {
		t.Fatalf("reset did not restore the solid background: %g", a.paneOpacity)
	}
	for _, tc := range []struct {
		data string
		want float32
	}{
		{`{"version":2,"active":-1,"terminal_opacity":0}`, 0},
		{`{"version":2,"active":-1,"terminal_opacity":0.45}`, 0.45},
		{`{"version":4,"active":-1,"pane_opacity":0.6,"terminal_opacity":0.2}`, 0.6},
	} {
		if err := os.MkdirAll(filepath.Dir(a.statePath()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(a.statePath(), []byte(tc.data), 0o600); err != nil {
			t.Fatal(err)
		}
		next := New(a.opts)
		next.restore()
		if got := next.paneOpacity; got != tc.want {
			t.Errorf("restored opacity %g, want %g", got, tc.want)
		}
		if st := next.state(); st.PaneOpacity == nil || *st.PaneOpacity != tc.want || st.TerminalOpacity != nil {
			t.Errorf("pane opacity was not saved in the new format: %+v", st)
		}
		next.Close()
	}
}

func TestSettingsCodingAgentsInstallAndRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	a := settingsApp(t)
	defer a.Close()
	a.opts.Bin = "/usr/local/bin/larik"
	tt := ui.NewTester(a.settingsView, 960, 680)
	tt.Frame()
	if err := tt.Click("Show Coding agents settings"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Desktop control skills") || !tt.HasText("Claude Code status") || !tt.HasText("Codex CLI status") || !tt.HasText("OpenCode status") || !tt.HasText("Gemini CLI status") {
		t.Fatalf("integration controls missing: %v", tt.Texts())
	}
	if err := tt.Click("Install skills"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !integration.SkillsInstalled(home) || !tt.HasText("Remove skills") {
		t.Fatalf("skills install failed: %v", tt.Texts())
	}
	for _, agent := range integration.Agents() {
		if err := tt.Click("Install " + agent); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		installed, err := integration.HookInstalled(home, agent)
		if err != nil || !installed || !tt.HasText("Remove "+agent) {
			t.Fatalf("%s install failed: %v %v %v", agent, installed, err, tt.Texts())
		}
	}
	if !tt.HasText("review and trust it in Codex /hooks") {
		t.Fatalf("Codex trust instruction missing: %v", tt.Texts())
	}
	for _, agent := range integration.Agents() {
		if err := tt.Click("Remove " + agent); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		installed, err := integration.HookInstalled(home, agent)
		if err != nil || installed {
			t.Fatalf("%s remove failed: %v %v", agent, installed, err)
		}
	}
	if err := tt.Click("Remove skills"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if integration.SkillsInstalled(home) {
		t.Fatal("skills were not removed")
	}
}

func TestSettingsAgentDefaultsAndProjectOverride(t *testing.T) {
	a := settingsApp(t)
	defer a.Close()
	t.Setenv("OPENAI_API_KEY", "test-key")
	personalPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "larik", "config.json")
	if err := os.MkdirAll(filepath.Dir(personalPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(personalPath, []byte(`{"mode":"accept-edits"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	projectPath := config.LocalSettingsPath(a.opts.Cwd)
	if err := os.MkdirAll(filepath.Dir(projectPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectPath, []byte(`{"mode":"plan"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(a.settingsView, 960, 680)
	tt.Frame()
	if err := tt.Click("Show Agent defaults settings"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Saved personal default: accept-edits") || !tt.HasText("Effective in "+filepath.Base(a.opts.Cwd)+": plan") {
		t.Fatalf("override is unclear: %v", tt.Texts())
	}
	snapshot(t, tt, "settings-agent.png")
	if err := tt.Click("Default model ID"); err != nil {
		t.Fatal(err)
	}
	tt.Type("openai/gpt-5.5")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	personal, err := config.LoadPersonal()
	if err != nil {
		t.Fatal(err)
	}
	if personal.Model != "openai/gpt-5.5" {
		t.Fatalf("model saved as %q", personal.Model)
	}
	if err := tt.Click("Choose reasoning effort"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("High"); err != nil {
		t.Fatal(err, tt.Texts())
	}
	tt.Frame()
	personal, err = config.LoadPersonal()
	if err != nil {
		t.Fatal(err)
	}
	if personal.Effort != llm.EffortHigh {
		t.Fatalf("effort saved as %q", personal.Effort)
	}
	if a.settings.effective.Mode != "plan" {
		t.Fatal("project mode changed")
	}
}

func TestSettingsModelListAndErrors(t *testing.T) {
	a := settingsApp(t)
	defer a.Close()
	t.Setenv("OPENAI_API_KEY", "test-key")
	tt := ui.NewTester(a.settingsView, 960, 680)
	tt.Frame()
	updates := make(chan func(), 1)
	a.update = func(fn func()) { updates <- fn }
	a.settings.listModels = func(context.Context, *config.Config) ([]settingsModelChoice, error) {
		return []settingsModelChoice{{spec: "openai/gpt-5.5", model: providers.Model{ID: "gpt-5.5", Efforts: []llm.Effort{llm.EffortLow, llm.EffortHigh}}}}, nil
	}
	if err := tt.Click("Show Agent defaults settings"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Browse models…"); err != nil {
		t.Fatal(err)
	}
	select {
	case fn := <-updates:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("model list did not finish")
	}
	tt.Frame()
	if err := tt.Click("Use openai/gpt-5.5"); err != nil {
		t.Fatal(err, tt.Texts())
	}
	tt.Frame()
	if a.settings.personal.Model != "openai/gpt-5.5" {
		t.Fatal("listed model was not saved")
	}

	// A file at the config directory path makes a later write fail.
	bad := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(bad, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.settings.personal.ConfigDir = bad
	if err := tt.Click("Show Agent behavior settings"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("auto_compact"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Could not save:") {
		t.Fatalf("save error missing: %v", tt.Texts())
	}

	configPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "larik", "config.json")
	if err := os.WriteFile(configPath, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.refreshSettings()
	tt.Frame()
	if !tt.HasText("Could not load agent settings:") || !strings.Contains(a.settings.loadError, configPath) {
		t.Fatalf("load error missing: %v", tt.Texts())
	}
}
