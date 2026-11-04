package desktop

import (
	"runtime"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestParseKey(t *testing.T) {
	for in, want := range map[string]string{
		"Ctrl+Cmd+K":     "Ctrl+Cmd+K",
		"cmd+ctrl+k":     "Ctrl+Cmd+K",
		" Shift+Cmd+p ":  "Shift+Cmd+P",
		"⌥⌘X":            "", // no plus: not a key
		"Option+Cmd+←":   "Alt+Cmd+Left",
		"command+=":      "Cmd+=",
		"F5":             "F5",
		"Ctrl+Return":    "Ctrl+Enter",
		"Alt+Cmd+[":      "Alt+Cmd+[",
		"K":              "",
		"Shift+K":        "",
		"Hyper+K":        "",
		"Cmd+Banana":     "",
		"Cmd++":          "",
		"Ctrl+Shift+F13": "",
	} {
		_, _, got, err := parseKey(in)
		if want == "" {
			if err == nil {
				t.Errorf("parseKey(%q) = %q, want an error", in, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("parseKey(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

// A key given in Settings runs its command, in the menus too, and the
// command's own key doesn't any more; none takes it away; Reset gives it
// back; and a key another command has isn't given.
func TestCustomKeys(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the window's own keys differ off macOS")
	}
	a := New(Options{DataDir: t.TempDir()})
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if err := a.setKey("Toggle Sidebar", "ctrl+cmd+s"); err != nil {
		t.Fatal(err)
	}
	if a.keys["Toggle Sidebar"] != "Ctrl+Cmd+S" || menuKey(a, "Toggle Sidebar") != "Ctrl+Cmd+S" {
		t.Fatalf("keys %v, menu %q", a.keys, menuKey(a, "Toggle Sidebar"))
	}
	tt.Key(ui.Super, ui.KeyB)
	tt.Frame()
	if a.noSidebar {
		t.Fatal("the command's own key still runs it")
	}
	tt.Key(ui.Super|ui.Ctrl, ui.KeyS)
	tt.Frame()
	if !a.noSidebar {
		t.Fatal("the key given doesn't run it")
	}

	if err := a.setKey("Find…", "Cmd+K"); err == nil || !strings.Contains(err.Error(), "Search Sessions…") {
		t.Fatalf("a key taken: %v", err)
	}
	if err := a.setKey("Find…", "Cmd+B"); err != nil { // Toggle Sidebar's no longer
		t.Fatal(err)
	}
	if err := a.setKey("Search Sessions…", ""); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Super, ui.KeyK)
	tt.Frame()
	if a.focusSearch || menuKey(a, "Search Sessions…") != "" {
		t.Fatal("a command with no key ran by its own")
	}
	if st := a.state(); st.Keys["Search Sessions…"] != "" || st.Keys["Find…"] != "Cmd+B" {
		t.Fatalf("saved %v", st.Keys)
	}
	a.resetKey("Search Sessions…")
	if menuKey(a, "Search Sessions…") != "Cmd+K" {
		t.Fatal("Reset didn't give the key back")
	}
	// Giving a command its own key is no change of its own.
	if err := a.setKey("Find…", "Cmd+F"); err != nil || len(a.keys) != 1 {
		t.Fatalf("%v, keys %v", err, a.keys)
	}

	// Settings lists the commands, and takes a key typed into one.
	settings := ui.NewTester(a.settingsView, 520, 2400)
	settings.Frame()
	for _, want := range []string{"Keyboard", "Toggle Sidebar", "Undo Pane Layout"} {
		if !settings.HasText(want) {
			t.Fatalf("no %q in %q", want, settings.Texts())
		}
	}
	snapshot(t, settings, "settings-keys.png")
}

func menuKey(a *App, label string) string {
	for _, g := range a.menuBar() {
		for _, cmd := range g.items {
			if cmd.label == label {
				return cmd.key
			}
		}
	}
	return "missing"
}

func TestAppearanceSettings(t *testing.T) {
	a := New(Options{DataDir: t.TempDir()})
	var appeared []string
	a.appear = func(v string) { appeared = append(appeared, v) }
	tt := ui.NewTester(a.settingsView, 520, 600)
	tt.Frame()
	if err := tt.Click("Dark"); err != nil {
		t.Fatal(err, tt.Texts())
	}
	tt.Frame()
	if a.appearance != "dark" || len(appeared) != 1 || appeared[0] != "dark" {
		t.Fatalf("appearance %q, appeared %v", a.appearance, appeared)
	}
	if err := tt.Click("Accent: Purple"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.accent != "#a550a7" || a.palette(ui.LightTheme()).Accent != ui.Hex("#a550a7") {
		t.Fatalf("accent %q", a.accent)
	}
	snapshot(t, tt, "settings-appearance.png")
	if st := a.state(); st.Appearance != "dark" || st.Accent != "#a550a7" {
		t.Fatalf("saved %+v", st)
	}
	// The system's again.
	if err := tt.Click("System"); err != nil {
		t.Fatal(err)
	}
	tt.Click("Accent: System")
	tt.Frame()
	if a.appearance != "" || a.accent != "" || a.palette(ui.LightTheme()).Accent != ui.LightTheme().Accent {
		t.Fatalf("back to the system's: %q %q", a.appearance, a.accent)
	}
}
