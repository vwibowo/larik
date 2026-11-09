package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// fourPanes is one | two over three, with four beside them all:
//
//	+-----+-------+------+
//	|     |  two  |      |
//	| one +-------+ four |
//	|     | three |      |
//	+-----+-------+------+
func fourPanes(t *testing.T) (*App, []*tab) {
	t.Helper()
	a := New(Options{Cwd: "/start"})
	tabs := []*tab{{id: "1", dir: "/one"}, {id: "2", dir: "/two"}, {id: "3", dir: "/three"}, {id: "4", dir: "/four"}}
	a.tabs = slices.Clone(tabs)
	a.activate(tabs[0])
	a.showInSplit(tabs[1], false)
	a.showInSplit(tabs[2], true)
	// Four beside two and three: splitting splits a pane, so by hand.
	beside := &pane{percent: 50}
	twoThree := a.layout.second
	a.replace(twoThree, beside)
	four := &pane{tab: tabs[3], parent: beside}
	beside.first, beside.second, twoThree.parent = twoThree, four, beside
	a.focusPane(four)
	return a, tabs
}

func TestPaneTree(t *testing.T) {
	a, tabs := fourPanes(t)
	if got := a.shownTabs(); len(got) != 4 || got[0] != tabs[0] || got[1] != tabs[1] || got[2] != tabs[2] || got[3] != tabs[3] {
		t.Fatalf("panes: %v", got)
	}
	if a.focused() != tabs[3] {
		t.Fatalf("the new pane has the keyboard: %v", a.focused().id)
	}
	// Moving between them: by where they are.
	for _, step := range []struct {
		dx, dy int
		want   string
	}{
		{-1, 0, "2"}, // two and three are as near and as alongside: the first
		{0, 1, "3"},
		{0, -1, "2"},
		{-1, 0, "1"},
		{-1, 0, "1"}, // nothing further
		{1, 0, "2"},
		{0, 1, "3"},
		{1, 0, "4"},
		{0, 1, "4"},
	} {
		a.focusToward(step.dx, step.dy)
		if a.focused().id != step.want {
			t.Fatalf("moving %d,%d: %s, want %s", step.dx, step.dy, a.focused().id, step.want)
		}
	}

	// A tab chosen shows in the focused pane; one shown already takes the
	// keyboard to its pane.
	a.focusPane(a.paneOf(tabs[0]))
	a.activate(tabs[2])
	if a.focused() != tabs[2] || shownAt(a, 0) != tabs[0] {
		t.Fatal("activating a shown tab moves the keyboard to it")
	}

	// Zoom shows the focused pane alone, until the keyboard moves on.
	a.toggleZoom()
	if !a.zoomed {
		t.Fatal("zoom")
	}
	a.toggleZoom()
	if a.zoomed {
		t.Fatal("zoom again unzooms")
	}
	a.toggleZoom()
	a.activate(tabs[0])
	if a.zoomed || a.focused() != tabs[0] {
		t.Fatal("moving to another pane unzooms")
	}

	// Closing a pane also closes its item and gives room to its neighbour.
	for _, tb := range tabs {
		tb.exited = true
	}
	a.focusPane(a.paneOf(tabs[1]))
	a.closePane(a.focus)
	if a.shown(tabs[1]) || len(a.tabs) != 3 || a.focused() != tabs[2] || len(a.panes()) != 3 {
		t.Fatalf("closing two: focused %v, %d panes, %d items", a.focused().id, len(a.panes()), len(a.tabs))
	}
	if p := a.paneOf(tabs[2]); p.parent == nil || p.parent.below || p.parent.first != p {
		t.Fatal("three takes two's place, beside four")
	}
	// Closing a tab closes its pane.
	a.closeTab(tabs[3])
	if len(a.panes()) != 2 || a.shown(tabs[3]) {
		t.Fatalf("closing four's tab: %d panes", len(a.panes()))
	}
	// Opening an already visible item focuses its existing pane.
	a.focusPane(a.paneOf(tabs[0]))
	a.showInSplit(tabs[2], true)
	if len(a.panes()) != 2 || a.focused() != tabs[2] {
		t.Fatalf("focused existing pane: %d panes", len(a.panes()))
	}
	// The final pane can close, leaving the window empty.
	a.closePane(a.paneOf(tabs[2]))
	if len(a.panes()) != 1 || a.focused() != tabs[0] {
		t.Fatal("the neighbouring pane should remain")
	}
	a.closePane(a.focus)
	if len(a.panes()) != 0 || a.focused() != nil || len(a.tabs) != 0 {
		t.Fatal("closing the final pane should leave an empty window")
	}
}

func TestPanesSaved(t *testing.T) {
	a, tabs := fourPanes(t)
	a.layout.percent = 30
	index := map[*tab]int{tabs[0]: 0, tabs[1]: 1, tabs[3]: 2} // three isn't saved
	saved := savePanes(a.layout, index)
	data, _ := json.Marshal(saved)
	var back savedPane
	json.Unmarshal(data, &back)
	opened := map[int]*tab{0: tabs[0], 1: tabs[1], 2: tabs[3]}
	p := restorePanes(&back, opened, map[*tab]bool{})
	got := p.leaves()
	if len(got) != 3 || got[0].tab != tabs[0] || got[1].tab != tabs[1] || got[2].tab != tabs[3] || p.percent != 30 || p.below {
		t.Fatalf("restored: %s", data)
	}
	if p.second.below || p.second.first.tab != tabs[1] || p.second.parent != p {
		t.Fatalf("three's room went to two, beside four: %s", data)
	}
	// A tab that didn't open, or shows twice, is left out.
	p = restorePanes(&back, map[int]*tab{0: tabs[0], 1: tabs[0]}, map[*tab]bool{})
	if got := p.leaves(); len(got) != 1 || got[0].tab != tabs[0] || p.parent != nil {
		t.Fatalf("with what didn't open left out: %d", len(got))
	}
}

func TestEarlierSplitRestores(t *testing.T) {
	needTerminal(t)
	data, repo := fixture(t)
	projects := index(t, data)
	var paths []string
	for _, p := range projects {
		paths = append(paths, p.Sessions[0].Path)
	}
	// As the one split of earlier versions saved it.
	old, _ := json.Marshal(map[string]any{"tabs": paths[:2], "active": 0, "right": 1, "split": 30})
	os.MkdirAll(filepath.Join(data, "desktop"), 0o700)
	os.WriteFile(filepath.Join(data, "desktop", "state.json"), old, 0o600)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data, Cwd: repo})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	a.setProjects(projects)
	if !a.restore() || len(a.panes()) != 2 || a.layout.percent != 30 || a.layout.below || shownAt(a, 0).path != paths[0] || shownAt(a, 1).path != paths[1] || a.focused() != shownAt(a, 0) {
		t.Fatalf("restored: %d panes", len(a.panes()))
	}
	// Saved again, as panes.
	if st := a.state(); st.Panes == nil || len(st.Panes.Panes) != 2 || st.Right != 0 || st.SplitP != 0 {
		t.Fatalf("saved: %+v", st)
	}
}

func TestPanesInTheWindow(t *testing.T) {
	needTerminal(t)
	data, repo := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data, Cwd: repo})
	defer a.Close()
	mu.Lock()
	a.setProjects(index(t, data))
	a.selectStart()
	mod, below := ui.Ctrl|ui.Shift, ui.Ctrl|ui.Shift|ui.Alt
	if runtime.GOOS == "darwin" {
		mod, below = ui.Super, ui.Super|ui.Shift
	}
	tt := ui.NewTester(a.view, 1400, 860)
	tt.SetDark(true)
	tt.Frame()
	tt.Key(mod, ui.KeyD)
	tt.Frame()
	tt.Key(below, ui.KeyD)
	tt.Frame()
	if len(a.panes()) != 3 || !a.layout.second.below || a.focused() != shownAt(a, 2) {
		mu.Unlock()
		t.Fatalf("%s then %s: %d panes", keyLabel(false, "D"), keyText(splitBelowKey()), len(a.panes()))
	}
	tabs := a.shownTabs()
	mu.Unlock()
	frameUntil(t, mu, tt, "every pane's larik", func() bool {
		for _, tb := range tabs {
			if !strings.Contains(tb.term.Text(), "args:") {
				return false
			}
		}
		return true
	})
	mu.Lock()
	defer mu.Unlock()
	tt.Frame()
	if !tt.HasText("Workspace") || !tt.HasText("3 panes") {
		t.Fatal("split layout needs workspace navigation")
	}
	if _, ok := tt.Find("Refactor the parser and run the tests tab"); ok {
		t.Fatal("split layout repeats a pane title in the tab strip")
	}
	snapshot(t, tt, "panes-dark.png")
	// The keys move between them.
	tt.Key(mod|ui.Alt, ui.KeyUp)
	tt.Frame()
	if a.focused() != tabs[1] {
		t.Fatalf("up: %v", a.tabTitle(a.focused()))
	}
	tt.Key(mod|ui.Alt, ui.KeyLeft)
	tt.Frame()
	if a.focused() != tabs[0] {
		t.Fatalf("left: %v", a.tabTitle(a.focused()))
	}
	// Zoomed, the focused pane shows alone, with a compact toolbar action.
	tt.Key(mod|ui.Shift, ui.KeyEnter)
	tt.Frame()
	if !a.zoomed || !tt.HasText("Show All Panes ("+keyText("Shift+Cmd+Enter")+")") || tt.HasText("Zoomed · 3 panes") {
		t.Fatalf("zoom: %v %q", a.zoomed, tt.Texts())
	}
	snapshot(t, tt, "panes-zoomed-dark.png")
	if err := tt.Click("Show All Panes (" + keyText("Shift+Cmd+Enter") + ")"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.zoomed || len(a.panes()) != 3 {
		t.Fatal("unzoom")
	}
	// Saved as panes: those whose tabs have sessions to resume, here
	// the first only.
	if st := a.state(); st.Panes == nil || len(st.Panes.Panes) != 0 || st.Active != 0 {
		t.Fatalf("saved: %+v", st.Panes)
	}
}
