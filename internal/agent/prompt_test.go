package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMinimalContextDropsGlobalInstructions(t *testing.T) {
	cwd, configDir := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(configDir, "AGENTS.md"), []byte("global rule"), 0o644)
	os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("project rule"), 0o644)

	full := ContextSections(cwd, configDir)
	if !strings.Contains(full, "global rule") || !strings.Contains(full, "project rule") {
		t.Fatalf("ContextSections should include both files:\n%s", full)
	}

	minimal := MinimalContextSections(cwd)
	if strings.Contains(minimal, "global rule") {
		t.Errorf("MinimalContextSections should drop the global file:\n%s", minimal)
	}
	if !strings.Contains(minimal, "project rule") {
		t.Errorf("MinimalContextSections should keep the project's own file:\n%s", minimal)
	}
	if !strings.Contains(minimal, "<env>") {
		t.Errorf("MinimalContextSections should still include the env block:\n%s", minimal)
	}
}

// TestPromptSizeBudget caps the fixed prompt text sent with every request
// (the plan-mode note with every prompt in plan mode). Growing it costs
// tokens on every turn of every session, so it should be a decision: if a
// change needs more room, raise the limit in the same change and say why
// in its message. Sizes are bytes, about four per token.
func TestPromptSizeBudget(t *testing.T) {
	for _, c := range []struct {
		name  string
		text  string
		limit int
	}{
		{"basePrompt (with SafetySection)", basePrompt, 1800},
		{"planModeNote with run_code", planModeNote + planCodeClause, 450},
	} {
		if len(c.text) > c.limit {
			t.Errorf("%s is %d bytes, over its %d-byte budget: trim it, or raise the limit on purpose", c.name, len(c.text), c.limit)
		}
	}
}
