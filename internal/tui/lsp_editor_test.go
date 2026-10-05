package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"larik/internal/lsp"
)

func TestLSPEditorPersonalOnlyAndOpaqueFields(t *testing.T) {
	m := testModel(t)
	path := m.opts.Config.UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"lsp":{"gopls":{"initialization_options":{"staticcheck":true},"unknown":"keep"},"zls":{"command":["zls"],"extensions":[".zig"],"root_markers":["build.zig"],"env":{"KEEP":"yes"},"initialization_options":[1,2]}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	m.opts.Config.LSP = map[string]lsp.ServerConfig{"project": {Command: []string{"danger"}, Extensions: []string{".x"}}}
	m.command("/config lsp")
	e := m.settings.lsp
	if e == nil || len(e.servers) != 2 || strings.Contains(m.lspEditorView(), "danger") {
		t.Fatal("editor copied merged project commands")
	}
	if !strings.Contains(m.lspEditorView(), "initialization_options: edit only in") {
		t.Fatal("missing precise file-only hint")
	}
	e.name = "gopls"
	e.rebuild()
	e.change("disabled", true)
	e.name = "zls"
	e.rebuild()
	e.change("env:KEEP", "changed")
	e.change("language_id", "zig")
	m.saveLSPEditor()
	data := savedConfig(t, m)
	for _, want := range []string{`"staticcheck": true`, `"unknown": "keep"`, `"initialization_options": [`, `"KEEP": "changed"`, `"disabled": true`, `"language_id": "zig"`} {
		if !strings.Contains(data, want) {
			t.Errorf("missing %s in %s", want, data)
		}
	}
	if strings.Contains(data, "danger") {
		t.Fatal("shared command persisted")
	}
}

func TestLSPEditorCustomCRUDAndValidation(t *testing.T) {
	m := testModel(t)
	m.command("/lsp-config")
	e := m.settings.lsp
	e.startInput("name", m.width)
	e.input.SetValue("zls")
	e.finishInput()
	e.change("command", []string{"zls", "--stdio"})
	e.change("extensions", []string{".zig"})
	e.change("root_markers", []string{"build.zig"})
	e.startInput("env+", m.width)
	e.input.SetValue("ZLS_LOG")
	e.finishInput()
	if e.input == nil || e.editing != "env:ZLS_LOG" {
		t.Fatal("adding an environment key must prompt for its value")
	}
	e.input.SetValue("1")
	e.finishInput()
	m.saveLSPEditor()
	if !strings.Contains(savedConfig(t, m), `"ZLS_LOG": "1"`) {
		t.Fatal("custom server not saved")
	}
	m.command("/lsp-config")
	e = m.settings.lsp
	e.name = "zls"
	e.rebuild()
	e.change("env:ZLS_LOG", nil)
	e.change("command", []string{})
	m.saveLSPEditor()
	if !e.failed {
		t.Fatal("empty command accepted")
	}
	e.change("command", []string{"zls"})
	m.saveLSPEditor()
	if strings.Contains(savedConfig(t, m), "ZLS_LOG") {
		t.Fatal("env key not removed")
	}
	m.command("/lsp-config")
	e = m.settings.lsp
	e.name = "zls"
	e.rebuild()
	e.list.selectWhere(func(it pickItem) bool { return it.value == "delete" })
	m.Update(press(tea.KeyEnter))
	m.saveLSPEditor()
	if strings.Contains(savedConfig(t, m), `"zls"`) {
		t.Fatal("custom server not removed")
	}
}

func TestLSPEditorBuiltinsToggleViaKeys(t *testing.T) {
	m := testModel(t)
	m.command("/lsp-config")
	e := m.settings.lsp
	e.list.selectWhere(func(it pickItem) bool { return it.value == "gopls" })
	m.Update(press(tea.KeyEnter))
	e.list.selectWhere(func(it pickItem) bool { return it.value == "disabled" })
	m.Update(press(tea.KeyEnter))
	if !e.servers["gopls"].Disabled {
		t.Fatal("built-in not disabled")
	}
	m.saveLSPEditor()
	if !strings.Contains(savedConfig(t, m), `"disabled": true`) {
		t.Fatal("built-in switch not persisted")
	}
}
