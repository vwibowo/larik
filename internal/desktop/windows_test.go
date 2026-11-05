package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestAutomaticWindowCapacity(t *testing.T) {
	a := New(Options{})
	a.contentSize = func() (int, int) { return 1240, 800 }
	if a.panesTooSmall(4) || !a.panesTooSmall(5) {
		t.Fatal("a normal window should fit four panes before opening another")
	}
	a.contentSize = func() (int, int) { return 720, 480 }
	if !a.panesTooSmall(2) {
		t.Fatal("a narrow window should keep one usable pane")
	}
	a.noSidebar = true
	if a.panesTooSmall(2) {
		t.Fatal("hiding the sidebar should make room for another pane")
	}
}

func TestSavedPanesStayInTheirWindowDuringRestore(t *testing.T) {
	a := New(Options{})
	a.contentSize = func() (int, int) { return 720, 480 }
	a.restoring = true
	a.overflow = func(*tab) bool {
		t.Fatal("restoring a saved pane must not create another window")
		return true
	}
	one, two := &tab{id: "one", chooser: true}, &tab{id: "two", chooser: true}
	a.tabs = []*tab{one, two}
	a.activate(one)
	a.activate(two)
	if len(a.panes()) != 2 {
		t.Fatalf("restored window has %d panes, want 2", len(a.panes()))
	}
}

func TestWindowGroupReadsOwnedHostTabs(t *testing.T) {
	data := t.TempDir()
	main := New(Options{DataDir: data})
	dir := filepath.Join(data, "desktop")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for path, st := range map[string]savedState{
		"state.json":        {Version: 4, Windows: []string{"second"}},
		"state-second.json": {Version: 4, Hosted: map[int]string{0: "owned-tab"}},
	} {
		b, _ := json.Marshal(st)
		if err := os.WriteFile(filepath.Join(dir, path), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	g := newWindowGroup(main)
	if !slices.Equal(g.ids, []string{"second"}) || !main.ownedHostTabs["owned-tab"] {
		t.Fatalf("window ownership was not restored: %v, %v", g.ids, main.ownedHostTabs)
	}
}

func TestWindowGroupRoutesAndReportsOneFocus(t *testing.T) {
	main, child := New(Options{}), New(Options{})
	one, two := &tab{id: "main-pane", browser: true, url: blankPage}, &tab{id: "child-pane", browser: true, url: blankPage}
	main.tabs, child.tabs = []*tab{one}, []*tab{two}
	main.activate(one)
	child.activate(two)
	g := &windowGroup{
		main: main, active: child, ids: []string{"child"},
		children: map[string]*windowEntry{"child": {app: child}},
	}
	res := g.control(Request{Cmd: "list"})
	if len(res.Tabs) != 2 || res.Tabs[0].Focused || !res.Tabs[1].Focused {
		t.Fatalf("only the active window should report focus: %+v", res.Tabs)
	}
	res = g.control(Request{Cmd: "focus", Tab: one.id})
	if res.Error != "" || res.Tab == nil || res.Tab.ID != one.id {
		t.Fatalf("command did not reach the main window: %+v", res)
	}
}

func TestWindowNavigationWrapsAndKeepsTheActiveWindow(t *testing.T) {
	main, second, third := New(Options{}), New(Options{}), New(Options{})
	g := &windowGroup{
		main: main, active: second, ids: []string{"second", "third"},
		children: map[string]*windowEntry{"second": {app: second}, "third": {app: third}},
	}
	raised := 0
	main.raise = func() { raised++ }
	g.navigateWindow(second, 1)
	if g.activeApp() != third || raised != 0 {
		t.Fatal("next window did not select the third window")
	}
	g.navigateWindow(third, 1)
	if g.activeApp() != main || raised != 1 {
		t.Fatal("next window did not wrap to the main window")
	}
	if index, total := g.windowPosition(third); index != 3 || total != 3 {
		t.Fatalf("third window position = %d of %d", index, total)
	}
	g.navigateWindow(main, -1)
	if g.activeApp() != third {
		t.Fatal("previous window did not wrap to the third window")
	}
}

func TestClosingInactiveWindowKeepsCommandTarget(t *testing.T) {
	main, active, closing := New(Options{}), New(Options{}), New(Options{})
	g := &windowGroup{
		main: main, active: active, ids: []string{"active", "closing"},
		children: map[string]*windowEntry{"active": {app: active}, "closing": {app: closing}},
	}
	if g.removeChild("closing", closing) || g.activeApp() != active {
		t.Fatal("closing an inactive window changed the command target")
	}
	if len(g.ids) != 1 || g.ids[0] != "active" {
		t.Fatalf("closed window remained in saved list: %v", g.ids)
	}
	g.removeChild("active", active)
	if g.activeApp() != main {
		t.Fatal("closing the active window did not fall back to the primary window")
	}
}
