package desktop

import (
	"runtime"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"

	"larik/internal/session"
	"larik/internal/status"
)

func TestKeyText(t *testing.T) {
	want := map[string]string{"Shift+Cmd+G": "⇧⌘G", "Alt+Cmd+T": "⌥⌘T", "Cmd+,": "⌘,", "": ""}
	if runtime.GOOS != "darwin" {
		want = map[string]string{"Shift+Cmd+G": "Ctrl+Shift+G", "Alt+Cmd+T": "Ctrl+Alt+Shift+T", "Cmd+,": "Ctrl+Shift+,", "": ""}
	}
	for key, w := range want {
		if got := keyText(key); got != w {
			t.Errorf("%q: %q, want %q", key, got, w)
		}
	}
}

func labels(items []paletteItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.label)
	}
	return out
}

func TestPaletteItems(t *testing.T) {
	a := New(Options{})
	fix := session.Indexed{Status: status.Report{State: status.Idle}}
	fix.ID, fix.Path, fix.Cwd, fix.Title = "s1", "/s1.jsonl", "/work", "Fix the flaky test"
	docs := session.Indexed{Status: status.Report{State: status.Idle}}
	docs.ID, docs.Path, docs.Cwd, docs.Title, docs.Subdir = "s2", "/s2.jsonl", "/work/site", "Write the docs", "site"
	a.setProjects([]session.Project{{Root: "/work", Name: "work", Sessions: []session.Indexed{fix, docs}}})

	// With nothing focused: the menu bar's commands, then the sessions.
	got := labels(a.paletteItems())
	for _, want := range []string{"New Tab", "New Larik Session", "Toggle Sidebar", "Next Session Waiting", "Fix the flaky test", "Write the docs"} {
		if !slices.Contains(got, want) {
			t.Errorf("no %q in %q", want, got)
		}
	}
	for _, not := range []string{paletteLabel, "Search Sessions…", "Rename Session…", "Stage All", ""} {
		if slices.Contains(got, not) {
			t.Errorf("%q in %q", not, got)
		}
	}

	// A session's tab focused, and the changes open: theirs come first.
	one := &tab{id: "t1", dir: "/work", path: fix.Path}
	a.tabs = []*tab{one}
	a.activate(one)
	a.changes = &changes{loaded: true, branch: "larik/fix", base: "main", against: compareCommit,
		files: []*fileChange{{path: "a.go", code: " M"}, {path: "b.go", code: "M "}}}
	got = labels(a.paletteItems())
	if got[0] != "Rename Session…" || got[1] != "Fork into a New Tab" {
		t.Fatalf("the session's first: %q", got)
	}
	for _, want := range []string{"Stage All", "Unstage All", "Compare Since main", "Fix the flaky test"} {
		if !slices.Contains(got, want) {
			t.Errorf("no %q in %q", want, got)
		}
	}
	// One tab: the Window menu's tab list isn't repeated.
	if n := len(slices.DeleteFunc(slices.Clone(got), func(s string) bool { return s != a.tabTitle(one) })); n > 1 {
		t.Errorf("the tab shows %d times", n)
	}

	// Typing narrows it: every word, a label starting with it first.
	a.paletteQuery = "docs"
	if got := labels(a.paletteMatches()); len(got) != 1 || got[0] != "Write the docs" {
		t.Fatalf("docs: %q", got)
	}
	a.paletteQuery = "site docs" // the detail counts, the project and subfolder
	if got := labels(a.paletteMatches()); len(got) != 1 || got[0] != "Write the docs" {
		t.Fatalf("site docs: %q", got)
	}
	a.paletteQuery = "new"
	if got := labels(a.paletteMatches()); got[0] != "New Tab" {
		t.Fatalf("new: %q", got)
	}
	a.paletteQuery = "zzz"
	if got := a.paletteMatches(); len(got) != 0 {
		t.Fatalf("zzz: %q", labels(got))
	}
}

func TestPaletteKeys(t *testing.T) {
	a := New(Options{})
	var sessions []session.Indexed
	for _, title := range []string{"One", "Two", "Three", "Four", "Five", "Six", "Seven"} {
		s := session.Indexed{Status: status.Report{State: status.Idle}}
		s.ID, s.Path, s.Cwd, s.Title = title, "/"+title+".jsonl", "/work", title+" thing"
		sessions = append(sessions, s)
	}
	a.setProjects([]session.Project{{Root: "/work", Name: "work", Sessions: sessions}})
	// Before anything is typed, only the newest sessions.
	a.paletteOpen = true
	shown := 0
	for _, it := range a.paletteMatches() {
		if it.session {
			shown++
		}
	}
	if shown != paletteSessions {
		t.Fatalf("%d sessions with nothing typed", shown)
	}
	a.paletteOpen = false

	mod := ui.Ctrl | ui.Shift
	if runtime.GOOS == "darwin" {
		mod = ui.Super
	}
	tt := ui.NewTester(a.view, 1240, 800)
	tt.SetDark(true)
	tt.Frame()
	tt.Key(mod|ui.Shift, ui.KeyP)
	tt.Frame()
	if !a.paletteOpen || !tt.HasText("Toggle Sidebar") || !tt.HasText(keyText("Cmd+B")) {
		t.Fatalf("%s: open %v, %q", keyLabel(true, "P"), a.paletteOpen, tt.Texts())
	}
	snapshot(t, tt, "palette-dark.png")

	// Typed, chosen with Down and run with Enter: it closes first.
	tt.Type("sidebar")
	tt.Frame()
	if found := a.paletteMatches(); len(found) != 1 {
		t.Fatalf("sidebar: %q", labels(found))
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if a.paletteOpen || !a.noSidebar {
		t.Fatalf("enter: open %v, sidebar hidden %v", a.paletteOpen, a.noSidebar)
	}

	// Opened again it's empty; Down and Up move the pick, wrapping.
	tt.Key(mod|ui.Shift, ui.KeyP)
	tt.Frame()
	if !a.paletteOpen || a.paletteQuery != "" || a.palettePick != 0 {
		t.Fatalf("again: open %v, %q, %d", a.paletteOpen, a.paletteQuery, a.palettePick)
	}
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if a.palettePick != 2 {
		t.Fatalf("down twice: %d", a.palettePick)
	}
	tt.Key(0, ui.KeyUp)
	tt.Key(0, ui.KeyUp)
	tt.Key(0, ui.KeyUp)
	tt.Frame()
	if n := len(a.paletteMatches()); a.palettePick != n-1 {
		t.Fatalf("up past the top: %d of %d", a.palettePick, n)
	}
	// Escape closes it, running nothing.
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.paletteOpen || !a.noSidebar {
		t.Fatalf("escape: open %v, sidebar hidden %v", a.paletteOpen, a.noSidebar)
	}
	// The shortcut toggles it.
	tt.Key(mod|ui.Shift, ui.KeyP)
	tt.Frame()
	tt.Key(mod|ui.Shift, ui.KeyP)
	tt.Frame()
	if a.paletteOpen {
		t.Fatal("the shortcut didn't close it")
	}
}
