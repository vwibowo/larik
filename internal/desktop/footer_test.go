package desktop

import (
	"runtime"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"

	"larik/internal/session"
)

func TestPickSeveralSessions(t *testing.T) {
	data, _ := fixture(t)
	a, mu := serialApp(Options{DataDir: data})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	a.setProjects(index(t, data))
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	add := ui.Ctrl
	if runtime.GOOS == "darwin" {
		add = ui.Super
	}
	const (
		parser  = "Refactor the parser and run the tests"
		osc     = "Add OSC 7501 output"
		landing = "Polish the landing copy"
	)

	// Command-click chooses without opening; the footer shows what's
	// chosen once there's more than one.
	for _, title := range []string{parser, landing} {
		if err := tt.ClickWith(add, title); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	if len(a.tabs) != 0 {
		t.Fatalf("a choosing click opened %d tabs", len(a.tabs))
	}
	want := []string{pathOf(t, a, parser), pathOf(t, a, landing)}
	if !slices.Equal(a.picked, want) || !tt.HasText("2 selected") || tt.HasText("larik dev") {
		t.Fatalf("picked %v, texts %q", a.picked, tt.Texts())
	}

	// Command-click again takes one out; Shift-click then chooses those
	// from the one clicked last, as the sidebar lists them, as Finder.
	if err := tt.ClickWith(add, parser); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.ClickWith(ui.Shift, osc); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if want := []string{pathOf(t, a, parser), pathOf(t, a, osc)}; !slices.Equal(a.picked, want) {
		t.Fatalf("after a range: %v, want %v", a.picked, want)
	}
	snapshot(t, tt, "sidebar-picked.png")

	// A chosen session's menu acts on them all; another's, on it alone.
	if err := tt.RightClick(landing); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(tt.Menu(), "Archive") {
		t.Fatalf("an unchosen session's menu: %q", tt.Menu())
	}
	if err := tt.RightClick(osc); err != nil {
		t.Fatal(err)
	}
	for _, item := range []string{"Open 2 Sessions", "Pin 2 Sessions to Top", "Archive 2 Sessions", "Move 2 Sessions to Trash…", "Deselect All"} {
		if !slices.Contains(tt.Menu(), item) {
			t.Errorf("menu lacks %q: %q", item, tt.Menu())
		}
	}
	if err := tt.ChooseMenuItem("Pin 2 Sessions to Top"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.pinned) != 2 {
		t.Fatalf("pinned %v", a.pinned)
	}

	// Escape, while the sidebar has the keyboard, lets go of them.
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if len(a.picked) != 0 || !tt.HasText("larik dev") {
		t.Fatalf("after Escape: picked %v", a.picked)
	}

	// The footer's archive button archives what's chosen.
	a.pinned = nil
	a.picked = []string{pathOf(t, a, landing), pathOf(t, a, parser)}
	tt.Frame()
	if err := tt.Click("Archive 2 sessions"); err != nil {
		t.Fatal(err, tt.Texts())
	}
	tt.Frame()
	if !session.IsArchived(pathOf(t, a, landing)) || !session.IsArchived(pathOf(t, a, parser)) || len(a.picked) != 0 {
		t.Fatalf("archived? picked %v, notice %q", a.picked, a.notice)
	}

	// A plain click chooses one alone again.
	a.picked = []string{pathOf(t, a, osc), "elsewhere"}
	if err := tt.Click(osc); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.picked) != 0 || !tt.HasText("larik dev") {
		t.Fatalf("after a plain click: picked %v", a.picked)
	}
}

func TestFooterShowsNewerRelease(t *testing.T) {
	data, _ := fixture(t)
	a, mu := serialApp(Options{DataDir: data, Version: "0.28.0"})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	a.setProjects(index(t, data))
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if !tt.HasText("larik v0.28.0") || tt.HasText("Inbox") || tt.HasText("all clear") {
		t.Fatalf("footer: %q", tt.Texts())
	}
	a.newer = "v0.29.0"
	tt.Frame()
	if !tt.HasText("v0.29.0 available") {
		t.Fatalf("no newer release in %q", tt.Texts())
	}
	if err := tt.Click("v0.29.0 available"); err != nil {
		t.Fatal(err)
	}
	if m := tt.Menu(); !slices.Contains(m, "What's New in v0.29.0") || !slices.Contains(m, "Copy Update Command") {
		t.Fatalf("update menu: %q", m)
	}
	snapshot(t, tt, "footer-update.png")
}

func TestDisplayVersion(t *testing.T) {
	for in, want := range map[string]string{"0.28.0": "v0.28.0", "v0.28.0": "v0.28.0", "": "dev", "dev": "dev"} {
		if got := displayVersion(in); got != want {
			t.Errorf("displayVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

// Shift+Up and Down in the sidebar choose sessions as Shift-clicks do,
// going on from the last one.
func TestPickWithKeyboard(t *testing.T) {
	data, _ := fixture(t)
	a, mu := serialApp(Options{DataDir: data})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	a.setProjects(index(t, data))
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	add := ui.Ctrl
	if runtime.GOOS == "darwin" {
		add = ui.Super
	}
	const parser = "Refactor the parser and run the tests"
	if err := tt.ClickWith(add, parser); err != nil { // gives the sidebar the keyboard
		t.Fatal(err)
	}
	tt.Frame()
	for range 2 {
		tt.Key(ui.Shift, ui.KeyDown)
		tt.Frame()
	}
	want := []string{pathOf(t, a, parser), pathOf(t, a, "Add OSC 7501 output"), pathOf(t, a, "Polish the landing copy")}
	if !slices.Equal(a.picked, want) || len(a.tabs) != 0 || !tt.HasText("3 selected") {
		t.Fatalf("picked %v, %d tabs", a.picked, len(a.tabs))
	}
	tt.Key(ui.Shift, ui.KeyUp)
	tt.Frame()
	if !slices.Equal(a.picked, want[:2]) {
		t.Fatalf("after Shift+Up: %v", a.picked)
	}
}
