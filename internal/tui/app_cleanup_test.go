package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"larik/internal/app"
)

func TestCleanupModelClosesCurrentAppBrowser(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	cwd := filepath.Join(root, "project")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config", "larik", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := "{\"model\":\"local/test\",\"providers\":{\"local\":{\"type\":\"openai-compatible\",\"base_url\":\"http://127.0.0.1:1/v1\"}},\"browser\":{\"enabled\":true}}"
	if err := os.WriteFile(configPath, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := app.Setup(cwd, "test")
	if err != nil {
		t.Fatal(err)
	}
	if a.Browser == nil {
		t.Fatal("browser session was not set up")
	}
	cleanupModel(&model{opts: Options{App: a}})
	if _, err := a.Browser.Serve(t.TempDir()); err == nil || !strings.Contains(err.Error(), "session is closed") {
		t.Fatalf("cleanup left the browser session open: %v", err)
	}
}
