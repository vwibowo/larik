package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/config"
	"larik/internal/llm"
	"larik/internal/providers"
)

func TestModelPickerEffortsFollowSelection(t *testing.T) {
	m := testModel(t)
	m.modelLists = map[string]providerModels{
		"ollama": {models: []providers.Model{
			{ID: "thinking", Chat: true, Thinking: true, CapsKnown: true},
			{ID: "plain", Chat: true, CapsKnown: true},
		}},
	}
	if cmd := m.openModelPicker(); cmd != nil {
		t.Fatal("opening picker with cached models should not relist providers")
	}
	mp := m.mpick
	selectModel := func(id string) {
		t.Helper()
		mp.list.selectWhere(func(it pickItem) bool { return it.value == (pickModel{"ollama", id}) })
		if it, ok := mp.list.selected(); !ok || it.value != (pickModel{"ollama", id}) {
			t.Fatalf("no row for %s", id)
		}
	}
	selectModel("thinking")
	if got := mp.selectedEfforts(m); !slices.Equal(got, []llm.Effort{"", "low", "medium", "high"}) {
		t.Fatalf("thinking levels: %v", got)
	}
	m.handleModelPickerKey(press(tea.KeyRight))
	m.handleModelPickerKey(press(tea.KeyRight))
	if mp.effort != 2 {
		t.Fatalf("effort index: %d", mp.effort)
	}
	m.handleModelPickerKey(press(tea.KeyDown)) // move from thinking to plain
	if it, _ := mp.list.selected(); it.value != (pickModel{"ollama", "plain"}) {
		t.Fatalf("wrong row: %+v", it)
	}
	if mp.effort != 0 || !strings.Contains(plain(m.modelPickerView()), "[default]") {
		t.Fatalf("plain model should offer default only: %q", plain(m.modelPickerView()))
	}
	m.handleModelPickerKey(press(tea.KeyRight))
	if mp.effort != 0 {
		t.Fatal("right must not select an unavailable level")
	}
}

func TestModelEffortAvailabilityAndSwitch(t *testing.T) {
	m := testModel(t)
	m.opts.Config.Providers["custom"] = config.ProviderConfig{Type: "openai-compatible"}
	m.modelLists = map[string]providerModels{"custom": {models: []providers.Model{{ID: "model", Efforts: []llm.Effort{llm.EffortHigh, llm.EffortLow}}}}}
	cases := []struct {
		provider, id string
		want         []llm.Effort
	}{
		{"anthropic", "claude-opus-5", []llm.Effort{"", "low", "medium", "high", "max"}},
		{"anthropic", "claude-haiku-4-5", []llm.Effort{"", "low", "medium", "high"}},
		{"gemini", "gemini-3.1-pro-preview", []llm.Effort{"", "low", "medium", "high"}},
		{"openai", "gpt-5.6-sol", []llm.Effort{"", "low", "medium", "high", "xhigh"}},
		{"openai", "gpt-4o", []llm.Effort{""}},
		{"openai", "gpt-5-chat-latest", []llm.Effort{""}},
		{"groq", "unknown", []llm.Effort{""}},
		{"custom", "model", []llm.Effort{"", "low", "high"}},
	}
	for _, tc := range cases {
		if got := m.availableEfforts(pickModel{tc.provider, tc.id}); !slices.Equal(got, tc.want) {
			t.Errorf("%s/%s: got %v, want %v", tc.provider, tc.id, got, tc.want)
		}
	}
	m.agent.SetEffort(llm.EffortMax)
	m.switchModel("ollama/plain", m.agent.Effort())
	if m.agent.Effort() != llm.EffortDefault {
		t.Fatalf("unsupported effort carried to plain model: %q", m.agent.Effort())
	}
}
