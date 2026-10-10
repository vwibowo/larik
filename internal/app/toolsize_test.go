package app

import (
	"bytes"
	"encoding/json"
	"testing"

	"larik/internal/tools"
)

// TestToolDefinitionBudget caps the built-in tool definitions (descriptions
// plus compacted schemas, as sent), which go with every request ahead of
// the conversation; see agent.TestPromptSizeBudget. LSP tools are left out:
// they appear only when a language server is installed. If a change needs
// more room, raise the limit in the same change and say why.
func TestToolDefinitionBudget(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	a, err := Setup(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	const limit = 10500 // 9807 on 2026-10-10
	total := 0
	for _, sp := range tools.NewRegistry(a.tools...).Specs() {
		if sp.Name == "lsp" || sp.Name == "apply_code_action" {
			continue
		}
		var schema bytes.Buffer
		if err := json.Compact(&schema, sp.Schema); err != nil {
			t.Fatalf("%s schema: %v", sp.Name, err)
		}
		size := len(sp.Description) + schema.Len()
		t.Logf("%-16s %5d bytes", sp.Name, size)
		total += size
	}
	if total > limit {
		t.Errorf("built-in tool definitions are %d bytes, over the %d-byte budget: trim them, or raise the limit on purpose", total, limit)
	}
}
