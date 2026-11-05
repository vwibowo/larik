package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOldHiddenItemsBecomePanes(t *testing.T) {
	data := t.TempDir()
	a := New(Options{DataDir: data, Cwd: t.TempDir()})
	defer a.Close()
	old := savedState{
		Version:  2,
		Tabs:     []string{"", ""},
		Browsers: map[int]string{0: "https://example.com", 1: "https://example.org"},
		Panes:    &savedPane{Tab: 0},
		Active:   0,
		Keys:     map[string]string{"New Tab": "Alt+Cmd+T"},
	}
	b, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(a.statePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.statePath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if !a.restore() || len(a.panes()) != 2 || a.focused() != a.tabs[0] {
		t.Fatalf("old hidden item was not tiled: %d panes", len(a.panes()))
	}
	if got := a.keys["New Pane"]; got != "Alt+Cmd+T" {
		t.Fatalf("shortcut was not migrated: %q", got)
	}
	if a.state().Version != 4 {
		t.Fatal("pane state version not saved")
	}
}
