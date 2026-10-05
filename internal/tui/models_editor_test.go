package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"larik/internal/config"
	"larik/internal/llm"
)

func TestModelsEditorCommandsAndSave(t *testing.T) {
	m := testModel(t)
	path := m.opts.Config.UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"models":{"custom":{"id":"custom","future":{"keep":true}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	m.command("/models-config")
	if m.settings == nil || m.settings.models == nil {
		t.Fatal("command did not open editor")
	}
	e := m.settings.models
	e.name = "custom"
	e.startInput("max_output", 80)
	e.input.SetValue("1024")
	e.finishInput()
	m.saveModelsEditor()
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), `"keep": true`) || !strings.Contains(string(b), `"max_output": 1024`) {
		t.Fatalf("save lost metadata: %s %v", b, err)
	}
	m.command("/config models")
	if m.settings == nil || m.settings.models == nil {
		t.Fatal("config action did not open editor")
	}
	m.command("/model")
	if m.mpick == nil {
		t.Fatal("/model picker changed")
	}
}

func TestModelsEditorDraftValidationAndPersonalRead(t *testing.T) {
	cfg := &config.Config{ConfigDir: t.TempDir(), Models: map[string]llm.ModelInfo{"shared": {ID: "shared"}}}
	e := newModelsEditor(cfg)
	if len(e.entries) != 0 || e.readFailed != nil {
		t.Fatalf("personal only: %+v", e)
	}
	e.startInput("name", 80)
	e.input.SetValue("provider/new")
	e.finishInput()
	if e.name != "provider/new" || e.changes[e.name] == nil {
		t.Fatalf("add draft: %+v", e)
	}
	e.startInput("context_window", 80)
	e.input.SetValue("-1")
	e.finishInput()
	if !e.failed || e.input == nil {
		t.Fatal("negative limit should keep input")
	}
	e.input.SetValue("4096")
	e.finishInput()
	if e.entries[e.name].ContextWindow != 4096 {
		t.Fatal("limit not set")
	}
	e.startInput("input_price", 80)
	e.input.SetValue("NaN")
	e.finishInput()
	if !e.failed {
		t.Fatal("NaN accepted")
	}
	e.input.SetValue("0.5")
	e.finishInput()
	if e.entries[e.name].InputPrice != 0.5 {
		t.Fatal("price not set")
	}
	if err := cfg.PatchUserModels(e.changes); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(cfg.UserConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatal("empty settings")
	}
	e2 := newModelsEditor(cfg)
	if len(e2.entries) != 1 || e2.entries["provider/new"].ContextWindow != 4096 {
		t.Fatalf("saved: %+v", e2.entries)
	}
	if spec, ok := settingByKey("models"); !ok || spec.cmd != "/models-config" {
		t.Fatalf("config action missing: ok=%v spec=%+v", ok, spec)
	}
}
