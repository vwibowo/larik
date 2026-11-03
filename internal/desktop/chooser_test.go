package desktop

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestNewTabAsksWhatToOpen(t *testing.T) {
	a := New(Options{Cwd: "/work"})
	s := &tab{id: "s", dir: "/work", path: "/s.jsonl"}
	a.tabs = []*tab{s}
	a.activate(s)

	a.openNew()
	ch := a.focused()
	if ch == nil || !ch.chooser || ch.dir != "/work" || len(a.tabs) != 2 || a.tabTitle(ch) != "New Tab" {
		t.Fatalf("cmd+t: %+v", ch)
	}
	tt := ui.NewTester(a.view, 1240, 800)
	tt.SetDark(true)
	tt.Frame()
	snapshot(t, tt, "chooser-dark.png")
	for _, want := range []string{"New Tab", "in /work", "Larik Session", "Session in a Worktree", "Terminal", "Browser"} {
		if !tt.HasText(want) {
			t.Fatalf("no %q in %q", want, tt.Texts())
		}
	}
	if !tt.Focused("Kinds of tab") {
		t.Fatal("the chooser takes the keyboard")
	}
	// Down, Down, Down and Enter: a browser tab, in the chooser's place.
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyDown)
	if ch.choice.pick != 3 {
		t.Fatalf("highlighted %d", ch.choice.pick)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	b := a.focused()
	if b == nil || !b.browser || b.dir != "/work" || len(a.tabs) != 2 || a.tabs[1] != b || a.hasTab(ch) {
		t.Fatalf("after choosing: %+v, %d tabs", b, len(a.tabs))
	}
}

func TestChooserInSplitAndItsLetters(t *testing.T) {
	a := New(Options{Cwd: "/work"})
	a.openBrowser("example.com") // drawn without a terminal, in a test
	s := a.focused()
	a.splitRight()
	ch := a.focused()
	if !ch.chooser || !a.split() || shownAt(a, 1) != ch {
		t.Fatal("cmd+d splits with a chooser")
	}
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	tt.Key(0, ui.KeyB)
	tt.Frame()
	if b := shownAt(a, 1); b == nil || !b.browser || shownAt(a, 0) != s || a.hasTab(ch) {
		t.Fatalf("B in the split: %+v", b)
	}
	// Closing a chooser starts nothing.
	a.openNew()
	ch = a.focused()
	a.closeTab(ch)
	if len(a.tabs) != 2 || a.hasTab(ch) {
		t.Fatalf("closing the chooser: %d tabs", len(a.tabs))
	}
}

func TestChooserTurnedOff(t *testing.T) {
	a := New(Options{Cwd: "/work"})
	a.noChooser = true
	if !a.state().NoChooser {
		t.Fatal("the setting is saved")
	}
	a.openNew() // starts larik, which fails here, with no binary: no chooser either way
	for _, tb := range a.tabs {
		if tb.chooser {
			t.Fatal("a chooser with it turned off")
		}
	}
}

func TestEmptyWindowChooses(t *testing.T) {
	a := New(Options{Cwd: "/work"})
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	snapshot(t, tt, "empty-light.png")
	if err := tt.Click("Browser"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.tabs) != 1 || !a.tabs[0].browser || a.focused() != a.tabs[0] {
		t.Fatalf("the empty window's choice: %d tabs", len(a.tabs))
	}
}

func TestArrowsWithThePointerOverTheChooser(t *testing.T) {
	a := New(Options{Cwd: "/work"})
	a.openNew()
	ch := a.focused()
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	r, ok := tt.Find("Larik Session")
	if !ok {
		t.Fatal("no chooser")
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2) // the pointer rests on the first kind
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if ch.choice.pick != 2 {
		t.Fatalf("Down twice under a resting pointer: %d", ch.choice.pick)
	}
	tt.Key(0, ui.KeyUp)
	tt.Frame()
	if ch.choice.pick != 1 {
		t.Fatalf("then Up: %d", ch.choice.pick)
	}
}

func TestArrowsAfterClickingTheChoosersBackground(t *testing.T) {
	a := New(Options{Cwd: "/work"})
	a.openNew()
	ch := a.focused()
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if err := tt.Click("New Tab"); err != nil { // its title, in the tab bar or the pane
		t.Fatal(err)
	}
	r, _ := tt.Find("↑ ↓ to choose, Enter to open")
	tt.ClickAt(r.X+r.W/2, r.Y+r.H+40) // below the list, in the pane
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if ch.choice.pick != 1 || !tt.Focused("Kinds of tab") {
		t.Fatalf("Down after a click in the pane: %d", ch.choice.pick)
	}
}
