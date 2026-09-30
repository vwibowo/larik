package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"larik/internal/memory"
)

func TestMemoryInThePromptAndTools(t *testing.T) {
	cfgHome, data, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("HOME", t.TempDir())

	a, err := Setup(cwd, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.Memory == nil {
		t.Fatal("memory is on by default")
	}
	hasTool := func(a *App) bool {
		for _, tl := range a.tools {
			if tl.Spec().Name == memory.ToolName {
				return true
			}
		}
		return false
	}
	if !hasTool(a) {
		t.Error("the memory tool should be offered")
	}
	if p := a.SystemPrompt(); !strings.Contains(p, "<memory>") || !strings.Contains(p, "No notes are saved yet.") {
		t.Errorf("the prompt should explain memory even when empty")
	}
	// A saved note is in the index of the next fresh context, outside the repo.
	if _, err := a.Memory.Save(memory.Note{Name: "uses-pnpm", Description: "This project uses pnpm, not npm", Body: "Run pnpm install."}); err != nil {
		t.Fatal(err)
	}
	if p := a.SystemPrompt(); !strings.Contains(p, "- uses-pnpm (project): This project uses pnpm, not npm") || strings.Contains(p, "Run pnpm install.") {
		t.Errorf("the next prompt should list the note, without its body")
	}
	project, _ := a.Memory.Dirs()
	if !strings.HasPrefix(project, filepath.Join(data, "larik", "memory")) {
		t.Errorf("notes belong under the data directory, not the project: %s", project)
	}
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Errorf("memory wrote into the project: %v", entries)
	}

	// Switched off: no tool, no prompt section.
	os.MkdirAll(filepath.Join(cfgHome, "larik"), 0o755)
	os.WriteFile(filepath.Join(cfgHome, "larik", "config.json"), []byte(`{"memory":{"enabled":false}}`), 0o644)
	off, err := Setup(cwd, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer off.Close()
	if off.Memory != nil || hasTool(off) || strings.Contains(off.SystemPrompt(), "<memory>") {
		t.Error("memory switched off should leave no tool and no prompt section")
	}
}
