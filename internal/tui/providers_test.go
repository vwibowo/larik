package tui

import (
	"errors"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/config"
	"larik/internal/providers"
)

func selectProvider(t *testing.T, m *model, name string) {
	t.Helper()
	m.provs.list.selectWhere(func(it pickItem) bool { return it.value == pickProvider{name} })
	if it, _ := m.provs.list.selected(); it.value != (pickProvider{name}) {
		t.Fatalf("no row for %s", name)
	}
}

func TestProvidersStatusAndRemoval(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	m := testModel(t)
	cfg := m.opts.Config
	if err := cfg.SaveProvider(cfg.UserConfigPath(), "vllm", config.ProviderConfig{Type: "openai-compatible", BaseURL: "http://127.0.0.1:9/v1"}, ""); err != nil {
		t.Fatal(err)
	}
	m.openProviders()
	m.Update(modelsLoadedMsg{map[string]providerModels{
		"ollama": {models: []providers.Model{{ID: "qwen3:4b", Chat: true}, {ID: "embed", CapsKnown: true}}},
		"vllm":   {err: errors.New("can't reach 127.0.0.1:9 (is the server running?)")},
	}})
	view := plain(m.providersView())
	for _, want := range []string{"ollama (in use)", "connected · 1 models", "vllm", "✗ unreachable"} {
		if !strings.Contains(view, want) {
			t.Errorf("providers view lacks %q:\n%s", want, view)
		}
	}

	selectProvider(t, m, "ollama")
	m.Update(typedKey('d'))
	if !strings.Contains(m.provs.note, "in use") {
		t.Fatalf("removing the provider in use must be refused: %q", m.provs.note)
	}

	selectProvider(t, m, "vllm")
	m.Update(typedKey('d'))
	if m.provs.confirm != "vllm" {
		t.Fatal("the first d should ask for confirmation")
	}
	m.Update(typedKey('d'))
	if _, ok := cfg.Providers["vllm"]; ok || !strings.Contains(m.provs.note, "removed vllm") {
		t.Fatalf("second d should remove it: %q", m.provs.note)
	}
	data, _ := os.ReadFile(cfg.UserConfigPath())
	if strings.Contains(string(data), "vllm") {
		t.Fatalf("vllm still saved:\n%s", data)
	}
}

func TestProvidersConfirmResetsOnOtherKey(t *testing.T) {
	m := testModel(t)
	cfg := m.opts.Config
	cfg.SaveProvider(cfg.UserConfigPath(), "vllm", config.ProviderConfig{Type: "openai-compatible", BaseURL: "http://127.0.0.1:9/v1"}, "")
	m.openProviders()
	selectProvider(t, m, "vllm")
	m.Update(typedKey('d'))
	m.Update(press(tea.KeyDown))
	m.Update(press(tea.KeyUp))
	m.Update(typedKey('d'))
	if _, ok := cfg.Providers["vllm"]; !ok {
		t.Fatal("a d after another key must ask again, not remove")
	}
}

func TestShortcutsOverlay(t *testing.T) {
	m := testModel(t)
	m.Update(typedKey('?'))
	if !m.showKeys || m.input.Value() != "" {
		t.Fatal("? on an empty prompt should open the overlay without typing")
	}
	if v := plain(m.shortcutsView()); !strings.Contains(v, "ctrl+o") || !strings.Contains(v, "alt+p") {
		t.Fatalf("overlay:\n%s", v)
	}
	m.Update(typedKey('x'))
	if m.showKeys || m.input.Value() != "" {
		t.Fatal("any key should close the overlay and be swallowed")
	}
	typeText(m, "why?")
	if m.showKeys || m.input.Value() != "why?" {
		t.Fatalf("? inside text is just a character: %q", m.input.Value())
	}
}
