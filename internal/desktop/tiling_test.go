package desktop

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestZoneAt(t *testing.T) {
	for _, tc := range []struct {
		x, y float32
		want zone
	}{
		{50, 50, zoneCenter},
		{5, 50, zoneLeft},
		{95, 50, zoneRight},
		{50, 5, zoneTop},
		{50, 95, zoneBottom},
		{5, 10, zoneLeft},    // a corner: the nearer edge
		{10, 5, zoneTop},     // the other way
		{29, 50, zoneLeft},   // inside the band
		{31, 50, zoneCenter}, // past it
	} {
		if got := zoneAt(tc.x, tc.y, 100, 100); got != tc.want {
			t.Errorf("zoneAt(%v, %v) = %v, want %v", tc.x, tc.y, got, tc.want)
		}
	}
	if zoneAt(1, 1, 0, 0) != zoneCenter {
		t.Error("an empty pane has only a center")
	}
}

// shape is the tree as text: a pane's tab id, or a split's two in
// brackets, | side by side and / one over the other.
func shape(p *pane) string {
	if !p.split() {
		return p.tab.id
	}
	sep := "|"
	if p.below {
		sep = "/"
	}
	return "(" + shape(p.first) + sep + shape(p.second) + ")"
}

// parents checks every pane's parent is the split holding it.
func parents(t *testing.T, p *pane) {
	t.Helper()
	if !p.split() {
		return
	}
	if p.first.parent != p || p.second.parent != p {
		t.Fatalf("parents wrong under %s", shape(p))
	}
	parents(t, p.first)
	parents(t, p.second)
}

func twoTabs() (*App, []*tab) {
	a := New(Options{Cwd: "/start"})
	tabs := []*tab{{id: "1", dir: "/one"}, {id: "2", dir: "/two"}, {id: "3", dir: "/three"}}
	a.tabs = tabs
	a.activate(tabs[0])
	return a, tabs
}

func TestDropTabOnEdges(t *testing.T) {
	for _, tc := range []struct {
		z    zone
		want string
	}{
		{zoneLeft, "(2|1)"},
		{zoneRight, "(1|2)"},
		{zoneTop, "(2/1)"},
		{zoneBottom, "(1/2)"},
		{zoneCenter, "2"},
	} {
		a, tabs := twoTabs()
		if !a.dropTab(tabs[1], a.layout, tc.z) {
			t.Fatalf("%v: nothing moved", tc.z)
		}
		if got := shape(a.layout); got != tc.want {
			t.Errorf("%v: %s, want %s", tc.z, got, tc.want)
		}
		if a.focused() != tabs[1] {
			t.Errorf("%v: the dropped tab should have the keyboard", tc.z)
		}
		parents(t, a.layout)
	}

	// A tab another pane shows leaves it for its new place.
	a, tabs := twoTabs()
	a.dropTab(tabs[1], a.layout, zoneRight)
	a.dropTab(tabs[2], a.paneOf(tabs[1]), zoneBottom)
	a.dropTab(tabs[2], a.paneOf(tabs[0]), zoneLeft)
	if got := shape(a.layout); got != "((3|1)|2)" {
		t.Fatalf("moved: %s", got)
	}
	// A pane's own tab does nothing to it.
	if a.dropTab(tabs[0], a.paneOf(tabs[0]), zoneRight) {
		t.Fatal("a pane split by its own tab")
	}
	// In the center, two panes swap.
	a.dropTab(tabs[2], a.paneOf(tabs[1]), zoneCenter)
	if got := shape(a.layout); got != "((2|1)|3)" {
		t.Fatalf("swapped: %s", got)
	}
	parents(t, a.layout)
}

func TestDropPane(t *testing.T) {
	a, tabs := fourPanes(t)
	one, four := a.paneOf(tabs[0]), a.paneOf(tabs[3])
	if a.dropPane(one, one, zoneRight) {
		t.Fatal("a pane dropped on itself moved")
	}
	if !a.dropPane(four, one, zoneTop) {
		t.Fatal("nothing moved")
	}
	if got := shape(a.layout); got != "((4/1)|(2/3))" {
		t.Fatalf("four above one: %s", got)
	}
	parents(t, a.layout)
}

func TestMovePane(t *testing.T) {
	a, tabs := fourPanes(t) // (1|((2/3)|4)), four focused
	a.focusPane(a.paneOf(tabs[1]))
	// A neighbour that way: the two swap, and the keyboard goes along.
	a.movePane(1, 0)
	if got := shape(a.layout); got != "(1|((4/3)|2))" {
		t.Fatalf("right: %s", got)
	}
	if a.focused() != tabs[1] {
		t.Fatal("the keyboard stays with the pane moved")
	}
	// None: to that edge, across the whole window.
	a.movePane(1, 0)
	if got := shape(a.layout); got != "((1|(4/3))|2)" {
		t.Fatalf("to the right edge: %s", got)
	}
	a.movePane(0, 1)
	if got := shape(a.layout); got != "((1|(4/3))/2)" {
		t.Fatalf("to the bottom edge: %s", got)
	}
	if a.focused() != tabs[1] || a.layout.percent != 50 {
		t.Fatalf("focused %v, percent %v", a.focused().id, a.layout.percent)
	}
	parents(t, a.layout)
}

func TestEqualize(t *testing.T) {
	a, _ := fourPanes(t) // (1|((2/3)|4))
	a.layout.percent, a.layout.second.percent, a.layout.second.first.percent = 80, 10, 90
	a.equalize()
	near := func(got, want float32) bool { return math.Abs(float64(got-want)) < 0.01 }
	// One beside two columns: a third; two over three: halves; four
	// beside two over three: halves.
	if !near(a.layout.percent, 100.0/3) || !near(a.layout.second.percent, 50) || !near(a.layout.second.first.percent, 50) {
		t.Fatalf("percents %v %v %v", a.layout.percent, a.layout.second.percent, a.layout.second.first.percent)
	}
}

func TestArrange(t *testing.T) {
	for _, tc := range []struct {
		l    layout
		want string
	}{
		{layoutColumns, "(1|(2|(3|4)))"},
		{layoutRows, "(1/(2/(3/4)))"},
		{layoutGrid, "((1|2)/(3|4))"},
		{layoutMainStack, "(3|(1/(2/4)))"},
	} {
		a, tabs := fourPanes(t)
		a.focusPane(a.paneOf(tabs[2]))
		a.zoomed = true
		a.arrange(tc.l)
		if got := shape(a.layout); got != tc.want {
			t.Errorf("%v: %s, want %s", tc.l, got, tc.want)
		}
		if a.focused() != tabs[2] || a.zoomed {
			t.Errorf("%v: focus %v, zoomed %v", tc.l, a.focused().id, a.zoomed)
		}
		parents(t, a.layout)
		// Each column of Columns takes a quarter.
		if tc.l == layoutColumns {
			rects := a.paneRects()
			for _, p := range a.panes() {
				if w := rects[p].x1 - rects[p].x0; math.Abs(float64(w-0.25)) > 0.001 {
					t.Errorf("column %s is %v wide", p.tab.id, w)
				}
			}
		}
		// Kept as laid out, as the window remembers it.
		index := map[*tab]int{}
		opened := map[int]*tab{}
		for i, tb := range a.tabs {
			index[tb], opened[i] = i, tb
		}
		if got := shape(restorePanes(savePanes(a.layout, index), opened, map[*tab]bool{})); got != tc.want {
			t.Errorf("%v restored as %s", tc.l, got)
		}
	}
	// Five in a grid: three, then two.
	a, tabs := fourPanes(t)
	five := &tab{id: "5", dir: "/five"}
	a.tabs = append(tabs, five)
	a.showInSplit(five, false)
	a.arrange(layoutGrid)
	if got := shape(a.layout); got != "((1|(2|3))/(4|5))" {
		t.Fatalf("five: %s", got)
	}
}

// A tab dragged from the tab bar onto a pane's right edge opens beside
// it; dragged by its header onto another's top, a pane moves there.
func TestDragTabToPaneEdge(t *testing.T) {
	needTerminal(t)
	data, _ := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	a.setProjects(index(t, data))
	a.selectSession(pathOf(t, a, "Polish the landing copy"))
	a.selectSession(pathOf(t, a, "Refactor the parser and run the tests"))
	site, parser := a.tabs[0], a.tabs[1]

	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	drag := func(from string, toX, toY func(ui.Rect) float32, to string) {
		t.Helper()
		src, ok := tt.Find(from)
		dst, ok2 := tt.Find(to)
		if !ok || !ok2 {
			t.Fatalf("no %q or %q: %q", from, to, tt.Texts())
		}
		x, y := toX(dst), toY(dst)
		tt.Press(src.X+20, src.Y+src.H/2)
		tt.Move(src.X+40, src.Y+src.H/2)
		tt.Move(x, y)
		tt.Frame()
		if a.drop.p == nil || a.drop.z != zoneRight {
			t.Fatalf("over the right edge, the drop is %+v", a.drop)
		}
		snapshot(t, tt, "drop-zone.png")
		tt.Release(x, y)
		tt.Frame()
	}
	right := func(r ui.Rect) float32 { return r.X + r.W - 20 }
	middle := func(r ui.Rect) float32 { return r.Y + r.H/2 }
	drag("Polish the landing copy tab", right, middle, "Pane: Refactor the parser and run the tests")
	if !a.split() || shape(a.layout) != "("+parser.id+"|"+site.id+")" {
		t.Fatalf("after the drop: split %v, %s", a.split(), shape(a.layout))
	}
	tt.Frame()
	top := func(r ui.Rect) float32 { return r.Y + 40 }
	centerX := func(r ui.Rect) float32 { return r.X + r.W/2 }
	src, _ := tt.Find("Pane: Polish the landing copy")
	tt.Press(src.X+60, src.Y+14) // its header
	tt.Move(src.X+80, src.Y+14)
	dst, _ := tt.Find("Pane: Refactor the parser and run the tests")
	tt.Move(centerX(dst), top(dst))
	tt.Frame()
	tt.Release(centerX(dst), top(dst))
	tt.Frame()
	if got := shape(a.layout); got != "("+site.id+"/"+parser.id+")" {
		t.Fatalf("after moving the pane by its header: %s", got)
	}
	snapshot(t, tt, "tiled.png")
}
