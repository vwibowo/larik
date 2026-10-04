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

// Claude Code reports effort support per model, so the picker offers exactly
// the levels its CLI would accept rather than one list for the whole provider.
func TestClaudeCLIEffortsFollowTheReportedModel(t *testing.T) {
	m := testModel(t)
	m.modelLists = map[string]providerModels{providers.ClaudeCLI: {models: []providers.Model{
		{ID: "sonnet", CapsKnown: true, Efforts: []llm.Effort{llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax}},
		{ID: "haiku", CapsKnown: true},
	}}}
	cases := []struct {
		id   string
		want []llm.Effort
	}{
		{"sonnet", []llm.Effort{"", "low", "medium", "high", "xhigh", "max"}},
		// Reported as taking no levels: offering any would be a setting the
		// CLI rejects.
		{"haiku", []llm.Effort{""}},
		// Typed in freehand, so nothing is known: keep the full range.
		{"claude-opus-9", []llm.Effort{"", "low", "medium", "high", "max"}},
	}
	for _, tc := range cases {
		if got := m.availableEfforts(pickModel{providers.ClaudeCLI, tc.id}); !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.id, got, tc.want)
		}
	}
}

// Two of Claude Code's models describe themselves identically, so the rows
// tell them apart only by the provider's own name for each.
func TestModelRowsCarryTheProvidersNameWhenItAddsSomething(t *testing.T) {
	const sonnet5 = "Sonnet 5 · Efficient for routine tasks"
	rows := modelItems([]providers.Model{
		{ID: "default", Display: "Default (recommended)", Desc: sonnet5, Context: 1_000_000, Chat: true, Tools: true, CapsKnown: true},
		{ID: "sonnet", Display: "Sonnet", Desc: sonnet5, Context: 1_000_000, Chat: true, Tools: true, CapsKnown: true},
		{ID: "claude-fable-5-1[1m]", Display: "Fable", Desc: "Fable 5.1 · Most capable", Context: 1_000_000, Chat: true, Tools: true, CapsKnown: true},
	}, "Claude Code CLI", "sonnet")
	if len(rows) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
	// The id is what gets selected and sent, so it stays the label.
	for i, want := range []string{"default", "sonnet", "claude-fable-5-1[1m]"} {
		if rows[i].label != want || rows[i].value != want {
			t.Errorf("row %d label/value = %q/%v, want %q", i, rows[i].label, rows[i].value, want)
		}
	}
	if rows[0].detail == rows[1].detail {
		t.Errorf("default and sonnet are indistinguishable: both read %q", rows[0].detail)
	}
	if !strings.HasPrefix(rows[0].detail, "Default (recommended) · "+sonnet5) {
		t.Errorf("default row = %q", rows[0].detail)
	}
	// "Sonnet" before "Sonnet 5 · …" and "Fable" before "Fable 5.1 · …" say
	// nothing the row does not already say.
	if !strings.HasPrefix(rows[1].detail, sonnet5) {
		t.Errorf("sonnet row repeats its own name: %q", rows[1].detail)
	}
	if !strings.HasPrefix(rows[2].detail, "Fable 5.1 · ") {
		t.Errorf("fable row repeats its own name: %q", rows[2].detail)
	}
}
