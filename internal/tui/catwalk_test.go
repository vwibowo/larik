package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/llm"
	"larik/internal/providers"
)

func TestCatwalkProviderIsOfferedInProviderAndModelPickers(t *testing.T) {
	defer providers.SetCatwalkProviders(nil)
	providers.SetCatwalkProviders([]llm.CatwalkProvider{{
		ID: "example-cloud", Name: "Example Cloud", Type: "openai-compatible",
		APIEndpoint: "https://api.example.test/v1", APIKey: "$EXAMPLE_CLOUD_KEY",
	}})

	m := testModel(t)
	m.openProviders()
	connect := pickConnect{"example-cloud"}
	m.provs.list.selectWhere(func(it pickItem) bool { return it.value == connect })
	if it, _ := m.provs.list.selected(); it.value != connect || !strings.Contains(it.label, "Example Cloud") {
		t.Fatalf("provider connect row = %+v", it)
	}
	m.handleProvidersKey(press(tea.KeyEnter))
	if m.wizard == nil || m.wizard.choice.Name != "example-cloud" || !m.wizard.choice.Catwalk {
		t.Fatal("Catwalk provider row did not open its connection wizard")
	}
	if len(m.wizard.fields) != 2 || m.wizard.fields[0].Value() != "https://api.example.test/v1" {
		t.Fatalf("wizard must show endpoint confirmation and key fields: %+v", m.wizard.fields)
	}

	m = testModel(t)
	m.openModelPicker()
	m.mpick.list.selectWhere(func(it pickItem) bool { return it.value == connect })
	if it, _ := m.mpick.list.selected(); it.value != connect || !strings.Contains(it.label, "Example Cloud") {
		t.Fatalf("model picker connect row = %+v", it)
	}
}
