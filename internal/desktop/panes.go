package desktop

import (
	"runtime"
	"time"

	"github.com/egoist/mygo/ui"
)

// Panes: the window shows tabs in panes, split side by side or one over
// the other, as many as fit, as Ghostty, iTerm and cmux split theirs.
// They're a tree: a split holds two, each a pane or a split of its own,
// and the share of the room the first takes, which dragging the divider
// changes. One pane has the keyboard, and the tab bar, the toolbar and
// the window's keys follow it: choosing a tab shows it there, or moves
// the keyboard to the pane already showing it. Closing a pane gives its
// room to its neighbour, and its tab stays open; closing a tab closes
// its pane, or, in the only one, shows the next tab there. Zooming shows
// the focused pane alone, for a while.

// pane is a pane, showing a tab, or a split of two.
type pane struct {
	tab *tab // a pane's
	// A split's: below puts second under first rather than beside it,
	// and percent is first's share of the room.
	below         bool
	percent       float32
	first, second *pane
	parent        *pane
}

func (p *pane) split() bool { return p.first != nil }

// leaves are the panes under p, left to right and top to bottom.
func (p *pane) leaves() []*pane {
	if p == nil {
		return nil
	}
	if !p.split() {
		return []*pane{p}
	}
	return append(p.first.leaves(), p.second.leaves()...)
}

// panes are the window's panes, in order.
func (a *App) panes() []*pane { return a.layout.leaves() }

// split reports whether the window shows more than one pane.
func (a *App) split() bool { return a.layout != nil && a.layout.split() }

// paneOf is the pane showing t, or nil.
func (a *App) paneOf(t *tab) *pane {
	for _, p := range a.panes() {
		if p.tab == t {
			return p
		}
	}
	return nil
}

// shown reports whether a pane shows t.
func (a *App) shown(t *tab) bool { return t != nil && a.paneOf(t) != nil }

// shownTabs are the tabs the panes show, in order.
func (a *App) shownTabs() []*tab {
	var out []*tab
	for _, p := range a.panes() {
		out = append(out, p.tab)
	}
	return out
}

// focused is the tab with the keyboard: the focused pane's.
func (a *App) focused() *tab {
	if a.focus == nil {
		return nil
	}
	return a.focus.tab
}

// focusPane gives p the keyboard, and marks its session read. Moving to
// a pane the zoom hides shows them all again.
func (a *App) focusPane(p *pane) {
	if p != a.focus {
		a.zoomed = false
	}
	a.focus, a.focusNext = p, p.tab
	if p.tab.browser {
		a.lastBrowser = p.tab
	}
	if p.tab.path != "" {
		a.seen[p.tab.path] = time.Now()
	}
	a.lookTab(p.tab)
}

// show shows t in p. A tab shows in one pane at a time: when another
// shows it, the two swap.
func (a *App) show(p *pane, t *tab) {
	if q := a.paneOf(t); q != nil && q != p {
		q.tab = p.tab
	}
	p.tab = t
}

// splitPane splits p in two, showing t in a new pane beside it, or below
// it, which takes the keyboard. A pane already showing t gives it up and
// closes; with no pane yet, t shows in the first.
func (a *App) splitPane(p *pane, t *tab, below bool) *pane {
	if q := a.paneOf(t); q != nil {
		if q == p || !a.split() {
			a.focusPane(q)
			return q
		}
		a.removePane(q)
	}
	if p == nil || a.paneOf(p.tab) != p {
		p = a.focus
	}
	if p == nil {
		a.layout = &pane{tab: t}
		a.focusPane(a.layout)
		return a.layout
	}
	n := &pane{below: below, percent: 50}
	a.replace(p, n)
	added := &pane{tab: t, parent: n}
	n.first, n.second, p.parent = p, added, n
	a.focusPane(added)
	return added
}

// replace puts n where old is in the tree.
func (a *App) replace(old, n *pane) {
	parent := old.parent
	switch {
	case parent == nil:
		a.layout = n
	case parent.first == old:
		parent.first = n
	default:
		parent.second = n
	}
	n.parent = parent
}

// removePane takes p out, its neighbour taking its room, and the
// keyboard if p had it. Its tab stays open.
func (a *App) removePane(p *pane) {
	parent := p.parent
	if parent == nil {
		a.layout, a.focus, a.zoomed = nil, nil, false
		return
	}
	next := parent.first
	if next == p {
		next = parent.second
	}
	a.replace(parent, next)
	if a.focus == p {
		// The pane beside it, or the nearest of the split beside it.
		near := next.leaves()
		if parent.first == p {
			a.focusPane(near[0])
		} else {
			a.focusPane(near[len(near)-1])
		}
	}
	if !a.split() {
		a.zoomed = false
	}
}

// closePane closes p, while there's another; its tab stays open.
func (a *App) closePane(p *pane) {
	if p != nil && a.split() {
		a.removePane(p)
	}
}

// splitNew starts larik in the focused tab's directory, in a new pane
// beside the focused one, or below it.
func (a *App) splitNew(below bool) {
	if a.focus == nil {
		a.openNew()
		return
	}
	var t *tab
	if a.noChooser {
		t = a.startTab(a.currentDir(), "", "")
	} else {
		t = a.newChooser(a.currentDir())
	}
	if t != nil {
		a.splitPane(a.focus, t, below)
	}
}

// splitBelowMods and splitBelowKey are the key splitting below takes:
// Shift+Command+D on macOS, as Ghostty's and iTerm's; elsewhere, where
// the window's keys take Control+Shift already (see shortcuts), Alt too.
func splitBelowMods() ui.Modifiers {
	if runtime.GOOS == "darwin" {
		return ui.Super | ui.Shift
	}
	return ui.Ctrl | ui.Shift | ui.Alt
}

func splitBelowKey() string {
	if runtime.GOOS == "darwin" {
		return "Shift+Cmd+D"
	}
	return "Alt+Cmd+D"
}

// splitRight is splitNew beside the focused pane.
func (a *App) splitRight() { a.splitNew(false) }

// showInSplit shows an open tab in a new pane beside the focused one, or
// moves the keyboard to it when a pane shows it already.
func (a *App) showInSplit(t *tab, below bool) {
	if a.focus == nil {
		a.activate(t)
		return
	}
	a.splitPane(a.focus, t, below)
}

// openInSplit shows the session at path in a new pane.
func (a *App) openInSplit(path string, below bool) {
	if t := a.sessionTab(path); t != nil {
		a.showInSplit(t, below)
	}
}

// toggleZoom shows the focused pane alone, or every pane again.
func (a *App) toggleZoom() {
	a.zoomed = !a.zoomed && a.split()
	if a.focus != nil {
		a.focusNext = a.focus.tab
	}
}

// rect is where a pane is in the window's panes, as fractions of their
// room.
type rect struct{ x0, y0, x1, y1 float32 }

// paneRects lays the panes out in a unit square, as the splits share it.
func (a *App) paneRects() map[*pane]rect {
	out := map[*pane]rect{}
	var lay func(p *pane, r rect)
	lay = func(p *pane, r rect) {
		if p == nil {
			return
		}
		if !p.split() {
			out[p] = r
			return
		}
		f, s := r, r
		if p.below {
			cut := r.y0 + (r.y1-r.y0)*p.percent/100
			f.y1, s.y0 = cut, cut
		} else {
			cut := r.x0 + (r.x1-r.x0)*p.percent/100
			f.x1, s.x0 = cut, cut
		}
		lay(p.first, f)
		lay(p.second, s)
	}
	lay(a.layout, rect{0, 0, 1, 1})
	return out
}

// focusToward moves the keyboard to the pane next to the focused one in
// the direction dx, dy (one of them -1 or 1): of those that side, the
// nearest, then the one most alongside it.
func (a *App) focusToward(dx, dy int) {
	if !a.split() || a.focus == nil {
		return
	}
	rects := a.paneRects()
	from := rects[a.focus]
	const eps = 1e-4
	var best *pane
	var bestGap, bestOverlap float32
	for _, p := range a.panes() {
		r := rects[p]
		var gap, overlap float32
		switch {
		case dx > 0 && r.x0 >= from.x1-eps:
			gap, overlap = r.x0-from.x1, min(r.y1, from.y1)-max(r.y0, from.y0)
		case dx < 0 && r.x1 <= from.x0+eps:
			gap, overlap = from.x0-r.x1, min(r.y1, from.y1)-max(r.y0, from.y0)
		case dy > 0 && r.y0 >= from.y1-eps:
			gap, overlap = r.y0-from.y1, min(r.x1, from.x1)-max(r.x0, from.x0)
		case dy < 0 && r.y1 <= from.y0+eps:
			gap, overlap = from.y0-r.y1, min(r.x1, from.x1)-max(r.x0, from.x0)
		default:
			continue
		}
		if overlap <= eps {
			continue // not alongside: diagonal
		}
		if best == nil || gap < bestGap-eps || gap < bestGap+eps && overlap > bestOverlap {
			best, bestGap, bestOverlap = p, gap, overlap
		}
	}
	if best != nil {
		a.focusPane(best)
	}
}

// activate shows t in the focused pane, or moves the keyboard to the
// pane showing it already, and marks its session read.
func (a *App) activate(t *tab) {
	if p := a.paneOf(t); p != nil {
		a.focusPane(p)
		return
	}
	if a.focus == nil {
		a.layout = &pane{tab: t}
		a.focus = a.layout
	}
	a.focus.tab = t
	a.focusPane(a.focus)
}

// unshow takes t, closing, out of the panes: its pane closes, or, as the
// only one, shows the tab beside it in the tab bar, at i now, or none.
func (a *App) unshow(t *tab, i int) {
	p := a.paneOf(t)
	switch {
	case p == nil:
	case a.split():
		a.removePane(p)
	case len(a.tabs) > 0:
		next := a.tabs[min(i, len(a.tabs)-1)]
		p.tab = next
		a.focusPane(p)
	default:
		a.layout, a.focus, a.zoomed = nil, nil, false
	}
}

// savedPane is the panes as the window remembers them: a pane, by its
// tab's index in savedState.Tabs, or a split of two.
type savedPane struct {
	Tab     int         `json:"tab"`
	Below   bool        `json:"below,omitempty"`
	Percent float32     `json:"percent,omitempty"`
	Panes   []savedPane `json:"panes,omitempty"`
}

// savePanes is p as saved, with tabs' indexes from index; panes whose
// tabs aren't saved are left out, their neighbours taking their room.
func savePanes(p *pane, index map[*tab]int) *savedPane {
	if p == nil {
		return nil
	}
	if !p.split() {
		i, ok := index[p.tab]
		if !ok {
			return nil
		}
		return &savedPane{Tab: i}
	}
	f, s := savePanes(p.first, index), savePanes(p.second, index)
	switch {
	case f == nil:
		return s
	case s == nil:
		return f
	}
	return &savedPane{Tab: -1, Below: p.below, Percent: p.percent, Panes: []savedPane{*f, *s}}
}

// restorePanes is the saved panes again, with the tabs opened by their
// index; those that didn't open are left out, and a tab shows once.
func restorePanes(sp *savedPane, opened map[int]*tab, used map[*tab]bool) *pane {
	if sp == nil {
		return nil
	}
	if len(sp.Panes) != 2 {
		t := opened[sp.Tab]
		if t == nil || used[t] {
			return nil
		}
		used[t] = true
		return &pane{tab: t}
	}
	f, s := restorePanes(&sp.Panes[0], opened, used), restorePanes(&sp.Panes[1], opened, used)
	switch {
	case f == nil:
		return s
	case s == nil:
		return f
	}
	percent := sp.Percent
	if percent <= 0 || percent >= 100 {
		percent = 50
	}
	n := &pane{below: sp.Below, percent: percent, first: f, second: s}
	f.parent, s.parent = n, n
	return n
}
