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
