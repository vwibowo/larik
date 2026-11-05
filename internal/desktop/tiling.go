package desktop

import (
	"math"
	"runtime"
	"slices"

	"github.com/egoist/mygo/ui"
)

// Tiling: the panes arranged as a tiling window manager arranges windows,
// as Rectangle does on macOS. A tab, a session from the sidebar, or a
// pane by its header, dragged over a pane, shows where it would go: the
// half at the edge it's near, which a drop splits off, or the whole pane,
// which it takes. The Window menu lays every pane out at once, in
// columns, rows, a grid, or one main pane beside a stack, and evens
// them; Control+Command and an arrow moves the focused pane that way,
// and Control+Command+Z takes back the last of these.

// zone is where on a pane something dragged over it goes.
type zone int

const (
	zoneCenter zone = iota // takes the pane's place
	zoneLeft
	zoneRight
	zoneTop
	zoneBottom
)

// zoneEdge is how far in from each side a pane's edge zones reach, as a
// share of its width or height.
const zoneEdge = 0.3

// zoneAt is the zone at x, y in a pane w by h: the edge nearest within
// zoneEdge of it, or the center.
func zoneAt(x, y, w, h float32) zone {
	if w <= 0 || h <= 0 {
		return zoneCenter
	}
	fx, fy := x/w, y/h
	best, near := zoneCenter, float32(zoneEdge)
	for _, e := range []struct {
		z zone
		d float32
	}{{zoneLeft, fx}, {zoneRight, 1 - fx}, {zoneTop, fy}, {zoneBottom, 1 - fy}} {
		if e.d < near {
			best, near = e.z, e.d
		}
	}
	return best
}

// split reports how a drop in z splits a pane: below or beside, the new
// pane before or after it. ok is false for the center.
func (z zone) split() (below, before, ok bool) {
	switch z {
	case zoneLeft:
		return false, true, true
	case zoneRight:
		return false, false, true
	case zoneTop:
		return true, true, true
	case zoneBottom:
		return true, false, true
	}
	return false, false, false
}

// where says, for a screen reader, where a drop in z put a pane.
func (z zone) where() string {
	return [...]string{"in place of", "to the left of", "to the right of", "above", "below"}[z]
}

// area is the part of a pane w by h that z shows it would take.
func (z zone) area(w, h float32) ui.Rect {
	switch z {
	case zoneLeft:
		return ui.Rect{W: w / 2, H: h}
	case zoneRight:
		return ui.Rect{X: w / 2, W: w / 2, H: h}
	case zoneTop:
		return ui.Rect{W: w, H: h / 2}
	case zoneBottom:
		return ui.Rect{Y: h / 2, W: w, H: h / 2}
	}
	return ui.Rect{W: w, H: h}
}

// paneDrag is a pane dragged by its header.
type paneDrag struct{ p *pane }

// dropping is where on which pane the last frame found something
// dragged: Drop has no position, the frames of DragOver before it do.
type dropping struct {
	p *pane
	z zone
}

// dropTab puts t where a drop in z on p goes: beside p, splitting it, or
// in p's place, where a tab another pane shows swaps with p's. It
// reports whether anything moved.
func (a *App) dropTab(t *tab, p *pane, z zone) bool {
	if t == nil || p == nil || p.tab == t {
		return false // a pane can't take, or split by, its own tab
	}
	a.remember()
	below, before, ok := z.split()
	if !ok {
		a.show(p, t)
		a.focusPane(a.paneOf(t))
		return true
	}
	if q := a.paneOf(t); q != nil {
		a.removePane(q)
	}
	a.zoomed = false
	a.splitAt(p, t, below, before)
	return true
}

// dropPane moves the pane src, dragged by its header, to z on p.
func (a *App) dropPane(src, p *pane, z zone) bool {
	if src == nil || p == nil || src == p || a.paneOf(src.tab) != src {
		return false
	}
	return a.dropTab(src.tab, p, z)
}

// movePane moves the focused pane by one in the direction dx, dy: it
// swaps with the pane next to it that way, or, with none there, goes to
// that edge of the window, across the whole of it.
func (a *App) movePane(dx, dy int) {
	p := a.focus
	if p == nil || !a.split() {
		return
	}
	a.remember()
	if q := a.paneToward(dx, dy); q != nil {
		p.tab, q.tab = q.tab, p.tab
		a.focusPane(q)
		return
	}
	t := p.tab
	a.removePane(p)
	n := &pane{tab: t}
	root := &pane{below: dy != 0, percent: 50}
	root.first, root.second = a.layout, n
	if dx < 0 || dy < 0 {
		root.first, root.second = n, a.layout
	}
	root.first.parent, root.second.parent = root, root
	a.layout, a.zoomed = root, false
	a.focusPane(n)
}

// equalize gives every pane the same share of the room along each
// split, counting the panes on each side of it that way.
func (a *App) equalize() {
	if !a.split() {
		return
	}
	a.remember()
	var even func(p *pane)
	even = func(p *pane) {
		if p == nil || !p.split() {
			return
		}
		f, s := across(p.first, p.below), across(p.second, p.below)
		p.percent = float32(100 * f / (f + s))
		even(p.first)
		even(p.second)
	}
	even(a.layout)
	a.zoomed = false
}

// across is how many panes p holds side by side, or one over the other
// with below: its splits that way add up, the others take the most of
// their two.
func across(p *pane, below bool) float64 {
	if !p.split() {
		return 1
	}
	f, s := across(p.first, below), across(p.second, below)
	if p.below == below {
		return f + s
	}
	return max(f, s)
}

// layout is one of the ways the Window menu lays the panes out.
type layout int

const (
	layoutColumns layout = iota
	layoutRows
	layoutGrid
	layoutMainStack
)

func (l layout) String() string {
	return [...]string{"Columns", "Rows", "Grid", "Main and Stack"}[l]
}

// arrange lays every pane out as l says, keeping their tabs, in their
// order, and the keyboard where it was. The main pane of Main and Stack
// is the focused one.
func (a *App) arrange(l layout) {
	if !a.split() {
		return
	}
	a.remember()
	focused := a.focused()
	ids := map[*tab]string{}
	for _, p := range a.panes() {
		ids[p.tab] = p.id
	}
	var tabs []*tab
	for _, p := range a.panes() {
		if l == layoutMainStack && p.tab == focused {
			tabs = append([]*tab{p.tab}, tabs...)
			continue
		}
		tabs = append(tabs, p.tab)
	}
	switch l {
	case layoutColumns:
		a.layout = chain(leavesOf(tabs), false)
	case layoutRows:
		a.layout = chain(leavesOf(tabs), true)
	case layoutGrid:
		cols := int(math.Ceil(math.Sqrt(float64(len(tabs)))))
		var rows []*pane
		for i := 0; i < len(tabs); i += cols {
			rows = append(rows, chain(leavesOf(tabs[i:min(i+cols, len(tabs))]), false))
		}
		a.layout = chain(rows, true)
	case layoutMainStack:
		main, stack := &pane{tab: tabs[0]}, chain(leavesOf(tabs[1:]), true)
		a.layout = &pane{percent: 60, first: main, second: stack}
		main.parent, stack.parent = a.layout, a.layout
	}
	a.layout.parent, a.zoomed = nil, false
	for _, p := range a.panes() {
		p.id = ids[p.tab]
		if p.tab == focused {
			a.focusPane(p)
		}
	}
}

func leavesOf(tabs []*tab) []*pane {
	out := make([]*pane, len(tabs))
	for i, t := range tabs {
		out[i] = &pane{tab: t}
	}
	return out
}

// chain splits panes evenly side by side, or one over the other with
// below: the first takes its share, and the rest split what's left.
func chain(panes []*pane, below bool) *pane {
	if len(panes) == 1 {
		return panes[0]
	}
	rest := chain(panes[1:], below)
	n := &pane{below: below, percent: 100 / float32(len(panes)), first: panes[0], second: rest}
	panes[0].parent, rest.parent = n, n
	return n
}

// arrangement is the panes as they were before a drop, a move or a
// layout changed them, for Undo Pane Layout to bring back.
type arrangement struct {
	layout *pane
	focus  *tab
	zoomed bool
}

// undoDepth is how many arrangements Undo Pane Layout goes back through.
const undoDepth = 20

// remember keeps the panes as they are, before a change to them.
func (a *App) remember() {
	if a.layout == nil {
		return
	}
	a.undo = append(a.undo, arrangement{clonePanes(a.layout, nil), a.focused(), a.zoomed})
	if len(a.undo) > undoDepth {
		a.undo = a.undo[len(a.undo)-undoDepth:]
	}
}

// undoLayout brings back the panes as they were before the last drop,
// move or layout. A tab closed since leaves its pane to its neighbour;
// an arrangement with none of its tabs left is skipped.
func (a *App) undoLayout() bool {
	for len(a.undo) > 0 {
		last := a.undo[len(a.undo)-1]
		a.undo = a.undo[:len(a.undo)-1]
		a.layout, a.focus = last.layout, nil
		for _, p := range a.panes() {
			if !slices.Contains(a.tabs, p.tab) {
				a.removePane(p)
			}
		}
		if a.layout == nil {
			continue
		}
		focus := a.panes()[0]
		if q := a.paneOf(last.focus); q != nil {
			focus = q
		}
		a.focusPane(focus)
		a.zoomed = last.zoomed && a.split()
		return true
	}
	return false
}

// clonePanes copies the tree at p, under parent.
func clonePanes(p, parent *pane) *pane {
	n := &pane{tab: p.tab, below: p.below, percent: p.percent, parent: parent}
	if p.split() {
		n.first, n.second = clonePanes(p.first, n), clonePanes(p.second, n)
	}
	return n
}

// tilingKey is a tiling command's key on macOS, Control+Command and a
// key, free there and beside Rectangle's Control+Option ones; elsewhere
// the window's keys take Control+Shift already, and these have none.
func tilingKey(key string) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	return "Ctrl+Cmd+" + key
}

// tilingCommands are the Window menu's pane layouts and moves, also in
// the command palette and the split button's menu.
func (a *App) tilingCommands() []command {
	cmds := []command{
		{label: "Equalize Panes", key: tilingKey("="), run: a.equalize},
	}
	for _, l := range []layout{layoutColumns, layoutRows, layoutGrid, layoutMainStack} {
		cmds = append(cmds, command{label: "Lay Out Panes in " + l.String(), run: func() { a.arrange(l) }})
	}
	return append(cmds,
		command{label: "Move Pane Left", key: tilingKey("Left"), run: func() { a.movePane(-1, 0) }},
		command{label: "Move Pane Right", key: tilingKey("Right"), run: func() { a.movePane(1, 0) }},
		command{label: "Move Pane Up", key: tilingKey("Up"), run: func() { a.movePane(0, -1) }},
		command{label: "Move Pane Down", key: tilingKey("Down"), run: func() { a.movePane(0, 1) }},
		command{label: "Undo Pane Layout", key: tilingKey("Z"), run: func() { a.undoLayout() }},
	)
}

// tilingShortcuts are the tiling commands' keys, on macOS.
func (a *App) tilingShortcuts(c *ui.Context) {
	if runtime.GOOS != "darwin" {
		return
	}
	mods := ui.Super | ui.Ctrl
	switch {
	case a.keyed(c, "Equalize Panes", mods, ui.KeyEqual):
		a.equalize()
		c.Announce("Panes evened out")
	case a.keyed(c, "Move Pane Left", mods, ui.KeyLeft):
		a.movePane(-1, 0)
	case a.keyed(c, "Move Pane Right", mods, ui.KeyRight):
		a.movePane(1, 0)
	case a.keyed(c, "Move Pane Up", mods, ui.KeyUp):
		a.movePane(0, -1)
	case a.keyed(c, "Move Pane Down", mods, ui.KeyDown):
		a.movePane(0, 1)
	case a.keyed(c, "Undo Pane Layout", mods, ui.KeyZ):
		if a.undoLayout() {
			c.Announce("Pane layout undone")
		}
	}
}

// tileable is what a pane takes dragged onto it: a tab from the tab bar,
// a session from the sidebar, or a pane by its header. An element takes
// one type of value, so the three share this one.
type tileable interface {
	// tileTab is the tab to place, opening the session's when it has
	// none, and the pane it comes from, if any.
	tileTab(a *App) (*tab, *pane)
	// from reports whether it is p's own, which p doesn't take.
	from(p *pane) bool
}

func (t *tab) tileTab(*App) (*tab, *pane) { return t, nil }
func (t *tab) from(p *pane) bool          { return p.tab == t }

func (s sessionDrag) tileTab(a *App) (*tab, *pane) { return a.sessionTab(string(s)), nil }
func (s sessionDrag) from(p *pane) bool            { return p.tab.path == string(s) }

func (d paneDrag) tileTab(*App) (*tab, *pane) { return d.p.tab, d.p }
func (d paneDrag) from(p *pane) bool          { return d.p == p }

// dropTarget makes e, showing pane p, take tabs, sessions and panes
// dragged onto it: while one is over it, the part it would take shows,
// and a drop puts it there.
func (a *App) dropTarget(c *ui.Context, t *ui.Theme, e ui.Element, p *pane) {
	if d, ok := ui.Drop[tileable](e); ok {
		z := zoneCenter
		if a.drop.p == p {
			z = a.drop.z
		}
		a.drop = dropping{}
		tb, src := d.tileTab(a)
		moved := false
		if src != nil {
			moved = a.dropPane(src, p, z)
		} else {
			moved = a.dropTab(tb, p, z)
		}
		if moved {
			c.Announce(a.tabTitle(tb) + " " + z.where() + " " + a.tabTitle(p.tab))
		}
	}
	d, ok := ui.DragOver[tileable](e)
	if !ok || d.from(p) {
		if a.drop.p == p {
			a.drop = dropping{}
		}
		return
	}
	x, y, _ := e.PointerPosition()
	b := e.Bounds()
	z := zoneAt(x, y, b.W, b.H)
	a.drop = dropping{p, z}
	tint, ring := t.Accent.Alpha(0.18), t.Accent
	e.DrawOver(func(pt *ui.Painter, r ui.Rect) {
		area := z.area(r.W, r.H)
		area.X, area.Y = area.X+r.X, area.Y+r.Y
		pt.Fill(area, tint, 0)
		pt.Stroke(area, ring, 0, 2)
	})
}
