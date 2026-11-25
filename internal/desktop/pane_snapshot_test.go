package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"larik/internal/desktop/panes"
	browserpane "larik/internal/desktop/panes/browser"
)

func TestUnknownSavedPaneLeavesOtherPanesUsable(t *testing.T) {
	a := New(Options{DataDir: t.TempDir(), Cwd: t.TempDir()})
	defer a.Close()
	st := savedState{
		Version: 6,
		Tabs:    []string{"", ""},
		Content: map[int]panes.Snapshot{
			0: {Kind: "removed", Dir: a.opts.Cwd},
			1: {Kind: panes.Browser, Dir: a.opts.Cwd, Data: paneData(browserpane.Snapshot{URL: "https://example.com"})},
		},
		Panes:  &savedPane{Tab: -1, Percent: 50, Panes: []savedPane{{Tab: 0}, {Tab: 1}}},
		Active: 0,
	}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(a.statePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.statePath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if !a.restore() || len(a.panes()) != 1 || a.focused() == nil || a.focused().browserState().URL != "https://example.com" {
		t.Fatalf("remaining browser pane was not restored: %d panes", len(a.panes()))
	}
}

func TestDisabledDatabaseCommands(t *testing.T) {
	previousVariants, previousRegistry := paneVariants, paneRegistry
	withoutDatabase := make([]paneVariant, 0, len(paneVariants)-1)
	for _, v := range paneVariants {
		if v.Kind != panes.Database {
			withoutDatabase = append(withoutDatabase, v)
		}
	}
	paneVariants, paneRegistry = withoutDatabase, paneRegistryFor(withoutDatabase)
	t.Cleanup(func() { paneVariants, paneRegistry = previousVariants, previousRegistry })
	a := New(Options{DataDir: t.TempDir(), Cwd: t.TempDir()})
	defer a.Close()
	for _, group := range a.menuDefaults() {
		for _, item := range group.items {
			if item.label == "New Database Pane" {
				t.Fatal("disabled Database pane remained in the menu")
			}
		}
	}
	for _, req := range []Request{{Cmd: "database", Op: "list"}, {Cmd: "open", Database: true, Cwd: a.opts.Cwd}} {
		if got := a.control(req).Error; got != "Database panes are unavailable" {
			t.Fatalf("%s: got %q", req.Cmd, got)
		}
	}
	st := savedState{
		Version:   5,
		Tabs:      []string{"", ""},
		Databases: map[int]string{0: a.opts.Cwd},
		Browsers:  map[int]string{1: "https://example.com"},
		Panes:     &savedPane{Tab: -1, Panes: []savedPane{{Tab: 0}, {Tab: 1}}},
	}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(a.statePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.statePath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if !a.restore() || len(a.panes()) != 1 || a.focused().browserState().URL != "https://example.com" {
		t.Fatal("legacy Database tab displaced the available browser pane")
	}
}
