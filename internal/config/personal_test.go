package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPersonalExcludesProjectLayers(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	personal := filepath.Join(configDir(), "config.json")
	if err := os.MkdirAll(filepath.Dir(personal), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(personal, []byte(`{"mode":"accept-edits"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	project := LocalSettingsPath(dir)
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{"mode":"plan"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	user, err := LoadPersonal()
	if err != nil {
		t.Fatal(err)
	}
	effective, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if user.Mode != "accept-edits" || effective.Mode != "plan" {
		t.Fatalf("personal %q, effective %q", user.Mode, effective.Mode)
	}
}
