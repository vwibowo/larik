package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/config"
	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/providers"
)

func persistentModel(t *testing.T) *model {
	t.Helper()
	m := testModel(t)
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	m.opts.Config.ConfigDir = filepath.Join(root, "larik")
	return m
}

func TestInteractiveDefaultsPersistAcrossReload(t *testing.T) {
	m := persistentModel(t)
	m.modelLists = map[string]providerModels{"ollama": {models: []providers.Model{{ID: "thinking", Chat: true, Thinking: true, CapsKnown: true}}}}
	m.switchModel("ollama/thinking", llm.EffortMedium)
	m.command("/mode plan")
	cfg, err := config.Load(m.opts.Config.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "ollama/thinking" || cfg.Effort != llm.EffortMedium || cfg.Mode != permission.ModePlan {
		t.Fatalf("reloaded defaults: model=%q effort=%q mode=%q", cfg.Model, cfg.Effort, cfg.Mode)
	}
	m.command("/effort default")
	m.cycleMode() // plan -> default
	cfg, err = config.Load(m.opts.Config.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "ollama/thinking" || cfg.Effort != "" || cfg.Mode != "" {
		t.Fatalf("clearing defaults: model=%q effort=%q mode=%q", cfg.Model, cfg.Effort, cfg.Mode)
	}
	m.command("/model ollama/plain")
	cfg, err = config.Load(m.opts.Config.Cwd)
	if err != nil || cfg.Model != "ollama/plain" || m.opts.Config.Model != cfg.Model {
		t.Fatalf("direct model switch must persist: %+v, %v", cfg, err)
	}
}

func TestPickerAndShortcutSaveDefaults(t *testing.T) {
	m := persistentModel(t)
	m.modelLists = map[string]providerModels{"ollama": {models: []providers.Model{{ID: "thinking", Chat: true, Thinking: true, CapsKnown: true}}}}
	m.openModelPicker()
	m.mpick.list.selectWhere(func(it pickItem) bool { return it.value == (pickModel{"ollama", "thinking"}) })
	m.mpick.syncEffort(m, llm.EffortDefault)
	m.handleModelPickerKey(press(tea.KeyRight))
	m.handleModelPickerKey(press(tea.KeyEnter))
	m.cycleMode() // default -> accept-edits
	cfg, err := config.Load(m.opts.Config.Cwd)
	if err != nil || cfg.Model != "ollama/thinking" || cfg.Effort != llm.EffortLow || cfg.Mode != permission.ModeAcceptEdits {
		t.Fatalf("picker/shortcut defaults: %+v, %v", cfg, err)
	}
}

func TestFailedSaveDoesNotChangeLiveDefaults(t *testing.T) {
	m := testModel(t)
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.opts.Config.ConfigDir = blocked
	m.command("/model ollama/other")
	m.command("/effort high")
	m.command("/mode plan")
	m.cycleMode()
	if m.agent.Model() != "m" || m.agent.Effort() != "" || m.agent.Perms().Mode() != permission.ModeDefault || m.opts.Config.Model != "" {
		t.Fatalf("failed writes changed session: model=%s effort=%s mode=%s config=%q", m.agent.Model(), m.agent.Effort(), m.agent.Perms().Mode(), m.opts.Config.Model)
	}
}
