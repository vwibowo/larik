package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/config"
	"larik/internal/hooks"
	"larik/internal/llm"
	"larik/internal/lsp"
)

// editorCase drives one native editor far enough to have an unsaved draft,
// which is what the cross-editor contracts below are about: every editor has
// to report saving and discarding in the same words, whatever it edits.
type editorCase struct {
	command string
	// change makes the editor dirty the way its own tests do.
	change func(m *model)
	status func(m *model) string
	state  savedState
}

// editorCases covers every editor /config can open. A new one belongs here, so
// that it has to agree with the others before it ships.
func editorCases() []editorCase {
	return []editorCase{
		{
			command: "/keybindings",
			change: func(m *model) {
				m.Update(press(tea.KeyEnter))
				m.settings.keybindings.input.SetValue("ctrl+enter")
				m.Update(press(tea.KeyEnter))
			},
			status: func(m *model) string { return m.settings.keybindings.status },
			state:  savedActive,
		},
		{
			command: "/permissions",
			change: func(m *model) {
				m.Update(press(tea.KeyEnter)) // + Add allow rule
				typeText(m, "bash(go test*)")
				m.Update(press(tea.KeyEnter))
			},
			status: func(m *model) string { return m.settings.rules.status },
			state:  savedActive,
		},
		{
			command: "/statusline",
			change: func(m *model) {
				m.Update(press(tea.KeyEnter)) // Command
				typeText(m, "printf hi")
				m.Update(press(tea.KeyEnter))
			},
			status: func(m *model) string { return m.settings.display.status },
			state:  savedActive,
		},
		{
			command: "/sidebar-config",
			change: func(m *model) {
				m.Update(press(tea.KeyEnter))
				typeText(m, "printf hi")
				m.Update(press(tea.KeyEnter))
			},
			status: func(m *model) string { return m.settings.display.status },
			state:  savedActive,
		},
		{
			command: "/sandbox-config",
			change:  func(m *model) { m.Update(press(tea.KeyEnter)) }, // enabled: default → on
			status:  func(m *model) string { return m.settings.sandbox.status },
			state:   savedAfterReload,
		},
		{
			command: "/web-search-config",
			change:  func(m *model) { m.settings.endpoint.change("provider", "tavily") },
			status:  func(m *model) string { return m.settings.endpoint.status },
			state:   savedAfterReload,
		},
		{
			command: "/stt-config",
			change:  func(m *model) { m.settings.endpoint.change("language", "id") },
			status:  func(m *model) string { return m.settings.endpoint.status },
			state:   savedAfterReload,
		},
		{
			command: "/tts-config",
			change:  func(m *model) { m.settings.endpoint.change("voice", "alloy") },
			status:  func(m *model) string { return m.settings.endpoint.status },
			state:   savedAfterReload,
		},
		{
			command: "/lsp-config",
			change: func(m *model) {
				e := m.settings.lsp
				e.list.selectWhere(func(it pickItem) bool { return it.value == "gopls" })
				m.Update(press(tea.KeyEnter))
				e.list.selectWhere(func(it pickItem) bool { return it.value == "disabled" })
				m.Update(press(tea.KeyEnter))
			},
			status: func(m *model) string { return m.settings.lsp.status },
			state:  savedAfterReload,
		},
		{
			command: "/mcp-config",
			change: func(m *model) {
				e := m.settings.mcp
				e.startInput("name", m.width)
				e.input.SetValue("local")
				e.finishInput()
				e.startInput("command", m.width)
				e.input.SetValue("npx -y server")
				e.finishInput()
			},
			status: func(m *model) string { return m.settings.mcp.status },
			state:  savedAfterReload,
		},
		{
			command: "/models-config",
			change: func(m *model) {
				e := m.settings.models
				e.startInput("name", m.width)
				e.input.SetValue("custom")
				e.finishInput()
				e.startInput("max_output", m.width)
				e.input.SetValue("1024")
				e.finishInput()
			},
			status: func(m *model) string { return m.settings.models.status },
			state:  savedAfterReload,
		},
		{
			command: "/hooks-config",
			change: func(m *model) {
				e := m.settings.hooks
				e.list.selectWhere(func(it pickItem) bool { return it.value == "Stop" })
				m.Update(press(tea.KeyEnter))
				e.list.selectWhere(func(it pickItem) bool { return it.value == "add" })
				m.Update(press(tea.KeyEnter))
				e.list.selectWhere(func(it pickItem) bool { return it.value == "command" })
				m.Update(press(tea.KeyEnter))
				e.startInput("command", m.width)
				e.input.SetValue("echo personal")
				e.finishInput()
			},
			status: func(m *model) string { return m.settings.hooks.status },
			state:  savedAfterReload,
		},
	}
}

// Every native editor reports a save in the same words, so you never have to
// work out from one editor's phrasing whether a setting is in force. There are
// only two outcomes: the running session uses it, or /reload does — and
// /reload costs the model's context, which the message says.
func TestEditorsReportSavingInTheSameWords(t *testing.T) {
	for _, tc := range editorCases() {
		t.Run(tc.command, func(t *testing.T) {
			m := testModel(t)
			m.command(tc.command)
			if m.settings == nil {
				t.Fatalf("%s did not open an editor", tc.command)
			}
			tc.change(m)
			m.Update(typedKey('s'))
			if m.settings == nil {
				t.Fatalf("%s closed its editor without reloading", tc.command)
			}
			// The scope-switching editors save wherever the cursor is; every
			// other editor is personal-only. Both start on the personal file.
			want := savedStatus(m.opts.Config.UserConfigPath(), tc.state)
			if got := tc.status(m); got != want {
				t.Errorf("status = %q, want %q", got, want)
			}
		})
	}
}

// Esc never throws away work silently: an editor holding an unsaved draft says
// so and waits for a second esc. Editors with sub-screens back out of those
// first, so the warning can come a few keys in — but it has to come before the
// editor closes, in every one of them.
func TestEditorsWarnBeforeDiscardingADraft(t *testing.T) {
	for _, tc := range editorCases() {
		t.Run(tc.command, func(t *testing.T) {
			m := testModel(t)
			m.command(tc.command)
			tc.change(m)
			warned := false
			for range 8 {
				if m.settings == nil {
					break
				}
				m.Update(press(tea.KeyEscape))
				if m.settings != nil && strings.Contains(tc.status(m), "Unsaved changes") {
					warned = true
				}
			}
			if m.settings != nil {
				t.Fatalf("esc did not close the editor")
			}
			if !warned {
				t.Error("esc discarded an unsaved draft without warning first")
			}

			// Nothing to lose, nothing to ask about: esc closes at once.
			clean := testModel(t)
			clean.command(tc.command)
			clean.Update(press(tea.KeyEscape))
			if clean.settings != nil {
				t.Error("esc should close an editor with no changes at once")
			}
		})
	}
}

// Saving is reported the same way whichever path it took: the editors that
// rebuild the services close and say so in the conversation, naming what they
// saved because the editor that would have named it is gone.
func TestSavedStatusWordsBothOutcomes(t *testing.T) {
	path := "/home/u/.config/larik/config.json"
	if got := savedStatus(path, savedActive); !strings.HasSuffix(got, " · active now") {
		t.Errorf("an applied save = %q", got)
	}
	got := savedStatus(path, savedAfterReload)
	if !strings.Contains(got, "/reload") || !strings.Contains(got, "fresh context") {
		t.Errorf("a pending save should name /reload and its cost: %q", got)
	}
	if reloaded := savedAndReloaded("MCP servers", path); !strings.HasPrefix(reloaded, "MCP servers saved to ") ||
		!strings.HasSuffix(reloaded, " · active now") {
		t.Errorf("a reloaded save should name what it saved: %q", reloaded)
	}
}

// A /config row for an editor that writes only your personal file must not
// report the merged count the session runs on: the editor would then open on
// fewer entries than the row promised.
func TestConfigRowsSeparatePersonalFromMergedCounts(t *testing.T) {
	m := testModel(t)
	cfg := m.opts.Config
	// What a project file or .mcp.json contributes: visible to the session,
	// not editable here.
	cfg.MCPServers = map[string]config.MCPServer{"shared": {Command: "x"}, "other": {Command: "y"}}
	cfg.LSP = map[string]lsp.ServerConfig{"shared": {Command: []string{"x"}}}
	cfg.Models = map[string]llm.ModelInfo{"shared": {}}
	cfg.TrustedHooks = hooks.Config{hooks.Stop: {{Hooks: []hooks.Command{{Type: "command", Command: "x"}}}}}

	for _, tc := range []struct{ key, want string }{
		{"mcp_servers", "0 personal · 2 servers in use"},
		{"lsp", "0 personal · 1 servers in use"},
		{"models", "0 personal · 1 overrides in use"},
		{"hooks", "0 personal · 1 events in use"},
	} {
		spec, ok := settingByKey(tc.key)
		if !ok {
			t.Fatalf("no %s row", tc.key)
		}
		if got := spec.get(m); got != tc.want {
			t.Errorf("%s row = %q, want %q", tc.key, got, tc.want)
		}
	}

	// With nothing but your own entries there is nothing to tell apart, so the
	// row stays a plain count.
	cfg.MCPServers = nil
	spec, _ := settingByKey("mcp_servers")
	if got := spec.get(m); got != "0 servers" {
		t.Errorf("row without merged entries = %q", got)
	}
}
