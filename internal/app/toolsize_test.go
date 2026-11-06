package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"larik/internal/status"
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

	const limit = 12500 // Desktop-specific tools appear only inside Desktop sessions.
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

func TestDatabaseToolsOnlyInDesktopSessions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	check := func(want bool) {
		t.Helper()
		a, err := Setup(t.TempDir(), "test")
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		count := 0
		for _, spec := range tools.NewRegistry(a.tools...).Specs() {
			if strings.HasPrefix(spec.Name, "desktop_db_") {
				count++
			}
			if strings.HasPrefix(spec.Name, "db_") {
				t.Fatalf("core database tool %q", spec.Name)
			}
		}
		if want && count != 4 || !want && count != 0 {
			t.Fatalf("Desktop=%v has %d Database tools", want, count)
		}
	}
	t.Setenv(status.TabEnv, "")
	check(false)
	t.Setenv(status.TabEnv, "pane-test")
	check(true)
}
