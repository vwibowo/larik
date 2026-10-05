package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatchUserLSPPreservesOpaqueAndSiblingFields(t *testing.T) {
	c := &Config{ConfigDir: t.TempDir()}
	path := c.UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"lsp":{"test":{"command":["old"],"initialization_options":{"nested":[1,2]},"future":true},"other":{"disabled":true}},"unrelated":7}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := LSPAt(path)
	if err != nil || got["test"].Command[0] != "old" {
		t.Fatalf("personal read: %v %v", got, err)
	}
	err = c.PatchUserLSP(map[string]map[string]any{"test": {"command": []string{"new"}, "env:KEY": "val"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"nested"`, `"future": true`, `"other"`, `"unrelated": 7`, `"KEY": "val"`, `"new"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s: %s", want, b)
		}
	}
}
