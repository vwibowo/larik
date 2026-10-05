package config

import (
	"os"
	"strings"
	"testing"

	"larik/internal/llm"
)

func TestPatchUserModelsPreservesUnknownAndValidates(t *testing.T) {
	c := &Config{ConfigDir: t.TempDir()}
	path := c.UserConfigPath()
	if err := os.WriteFile(path, []byte(`{"models":{"a":{"id":"a","provider":"openai","future":{"nested":true}},"other":{"id":"other"}},"sibling":42}`), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := UserModelsAt(path)
	if err != nil || entries["a"].Provider != "openai" {
		t.Fatalf("read: %v %v", entries, err)
	}
	a := entries["a"]
	a.ContextWindow = 8192
	a.InputPrice = 1.25
	added := llm.ModelInfo{ID: "provider/new", Provider: "provider", MaxOutput: 512}
	for _, invalid := range []llm.ModelInfo{{ID: "wrong"}, {ID: "a", ContextWindow: -1}, {ID: "a", InputPrice: -0.1}} {
		if err := c.PatchUserModels(map[string]*llm.ModelInfo{"a": &invalid}); err == nil {
			t.Fatal("accepted invalid model", invalid)
		}
	}
	if err := c.PatchUserModels(map[string]*llm.ModelInfo{"a": &a, "other": nil, "provider/new": &added}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"future"`, `"nested": true`, `"sibling": 42`, `"context_window": 8192`, `"input_price": 1.25`, `"provider/new"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s: %s", want, b)
		}
	}
	if strings.Contains(string(b), `"other"`) {
		t.Errorf("delete failed: %s", b)
	}
}
