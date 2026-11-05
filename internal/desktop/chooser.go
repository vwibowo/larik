package desktop

import (
	"slices"

	"github.com/egoist/mygo/ui"
)

// A new tab (cmd+t, or a split's cmd+d) shows a chooser first, as a
// browser's new tab page does, rather than starting larik: a Larik
// session, one in a new worktree, a terminal, a browser or a Files tab, in the
// folder it names, which Change… picks. Choosing one puts it in the
// chooser's place, in its pane and in the tab bar; closing the chooser
// starts nothing. Up and Down move between the kinds and Enter chooses,
// as do their numbers and letters. The window with no tab shows the same
// choices. The Settings window can turn the chooser off, and the new tab
// then starts larik, as before. The menus' own items (New Larik Session,
// New Terminal, …) start theirs at once.

// choice is a chooser's highlight: the kind picked, by the keys or the
// pointer, and the one the pointer was last over, -1 for none, so that a
// pointer resting on one doesn't take the highlight back from the keys.
type choice struct{ pick, hover int }

// tabKind is a kind of tab the chooser offers.
type tabKind struct {
	key   ui.Key // its letter
	title string
	about string
	icon  func() *ui.SVG
}

// tabKinds are the chooser's choices, in order.
var tabKinds = []tabKind{
	{ui.KeyL, "Larik Session", "Ask larik to work on this folder", func() *ui.SVG { return iconChat }},
	{ui.KeyW, "Session in a Worktree", "A session on a branch of its own, apart from your checkout", func() *ui.SVG { return iconBranch }},
	{ui.KeyT, "Terminal", "Your shell, or a dev server, beside the sessions", func() *ui.SVG { return iconTerm }},
	{ui.KeyB, "Browser", "A web page, such as what the dev server serves", func() *ui.SVG { return iconGlobe }},
	{ui.KeyF, "Files", "Browse this folder and preview files", func() *ui.SVG { return iconFolder }},
}

// newChooser opens a chooser tab in dir.
func (a *App) newChooser(dir string) *tab {
	a.notice = ""
	t := &tab{id: newTabID(), dir: dir, chooser: true, choice: choice{hover: -1}}
	a.tabs = append(a.tabs, t)
	return t
}

// openNew opens a new tab in the focused pane: the chooser, or larik
// with the chooser turned off.
func (a *App) openNew() {
	if a.noChooser {
		a.newTab(a.currentDir(), "", "")
		return
	}
	a.activate(a.newChooser(a.currentDir()))
}

// startKind starts a tab of the kind at i in dir, and returns it, or nil
// for a worktree session, which starts once its worktree is made, in the
// focused pane, or for one that couldn't start.
func (a *App) startKind(i int, dir string) *tab {
	switch i {
	case 0:
		return a.startTab(dir, "", "")
	case 1:
		a.newWorktreeSession(dir)
	case 2:
		return a.startShell(dir, "")
	case 3:
		if t := a.startBrowser(""); t != nil {
			t.dir = dir
			return t
		}
	case 4:
		return a.startFiles(dir)
	}
	return nil
}

// choose starts what the chooser ch offers at i, in its place.
func (a *App) choose(ch *tab, i int) {
	if p := a.paneOf(ch); p != nil {
		a.focusPane(p)
	}
	if i == 1 {
		// Made off the main thread; it shows in the pane the chooser's in,
		// which the chooser then leaves to it.
		a.newWorktreeSessionThen(ch.dir, func(t *tab) { a.replaceTab(ch, t) })
		return
	}
	if t := a.startKind(i, ch.dir); t != nil {
		a.replaceTab(ch, t)
	}
}

// replaceTab puts t, just started, where old is: in its pane, if one
// shows it, and in its place in the tab bar; old goes.
func (a *App) replaceTab(old, t *tab) {
	i, j := slices.Index(a.tabs, old), slices.Index(a.tabs, t)
	if j >= 0 && i >= 0 {
		a.tabs = slices.Delete(a.tabs, j, j+1)
		a.tabs[slices.Index(a.tabs, old)] = t
	} else if i >= 0 {
		a.tabs = slices.Delete(a.tabs, i, i+1)
	}
	if p := a.paneOf(old); p != nil {
		p.tab = t
		a.focusPane(p)
	} else if !a.shown(t) {
		a.activate(t)
	}
	if a.lastBrowser == old {
		a.lastBrowser = nil
	}
	if a.focusNext == old {
		a.focusNext = t
	}
}

// chooserPane is a chooser tab's pane.
func (a *App) chooserPane(c *ui.Context, t *ui.Theme, tb *tab) ui.Element {
	return a.chooser(c, t, "New Pane", "", &tb.choice, tb.dir, func(dir string) { tb.dir = dir }, func(i int) { a.choose(tb, i) })
}

// chooser offers the kinds of tab under title and hint, in dir, which
// Change… sets with setDir, the one ch picks highlighted, and gives
// choose the one chosen. It returns what takes the keyboard.
func (a *App) chooser(c *ui.Context, t *ui.Theme, title, hint string, ch *choice, dir string, setDir func(string), choose func(i int)) ui.Element {
	pick := &ch.pick
	*pick = min(max(*pick, 0), len(tabKinds)-1)
	chosen, hovered := -1, -1
	var list ui.Element
	wrap := ui.Column(c).Grow(1).Center().Gap(14).Padding(24).Children(func() {
		ui.Text(c, title).Bold().FontSize(17)
		if hint != "" {
			ui.Text(c, hint).FontSize(12).TextColor(t.TextMuted)
		}
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "in "+tildePath(dir)).FontSize(12).TextColor(t.TextMuted).SingleLine().Shrink(1)
			if a.pickDir != nil || isRemote(dir) {
				change := ui.Text(c.Key("change"), "Change…").FontSize(12).TextColor(t.Accent).Cursor(ui.CursorPointer).Label("Change the folder")
				if change.Clicked() {
					if isRemote(dir) {
						a.askRemoteFolder(dir, setDir)
					} else {
						a.pickDir(dir, setDir)
					}
				}
			}
		})
		list = ui.Column(c.Key("kinds")).Gap(4).Width(380).Shrink(1).Focusable().Label("Kinds of pane").Children(func() {
			for i, k := range tabKinds {
				row := ui.Row(c.Key(k.title)).Gap(12).Padding(10, 14).Radius(8).AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(k.title).Children(func() {
					ui.Icon(c, k.icon()).FontSize(18).TextColor(t.Accent)
					ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
						ui.Text(c, k.title).FontSize(13).Bold()
						ui.Text(c, k.about).FontSize(11).TextColor(t.TextMuted)
					})
					ui.Text(c, keyName(k.key)).FontSize(11).Font(mono).TextColor(t.TextMuted)
				})
				if i == *pick {
					row.Background(t.Accent.Alpha(0.12))
				}
				if row.Hovered() {
					hovered = i
				}
				if row.Clicked() {
					chosen = i
				}
			}
		})
		ui.Text(c, "↑ ↓ to choose, Enter to open").FontSize(11).TextColor(t.TextMuted)
	})
	// A click anywhere in it gives it the keyboard back, which a click
	// on nothing would take away.
	if wrap.Clicked() && !list.FocusWithin() {
		list.Focus()
	}
	// The pointer moving onto a kind picks it; resting there, it leaves
	// the keys to move the highlight.
	if hovered != ch.hover {
		ch.hover = hovered
		if hovered >= 0 {
			*pick = hovered
		}
	}
	if list.FocusWithin() {
		switch {
		case c.Shortcut(0, ui.KeyUp):
			*pick = (*pick + len(tabKinds) - 1) % len(tabKinds)
		case c.Shortcut(0, ui.KeyDown):
			*pick = (*pick + 1) % len(tabKinds)
		case c.Shortcut(0, ui.KeyEnter):
			chosen = *pick
		}
		for i, k := range tabKinds {
			if c.Shortcut(0, k.key) || c.Shortcut(0, []ui.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5}[i]) {
				chosen = i
			}
		}
	}
	if chosen >= 0 {
		choose(chosen)
	}
	return list
}

// keyName is how the chooser names a kind's letter.
func keyName(k ui.Key) string {
	switch k {
	case ui.KeyL:
		return "L"
	case ui.KeyW:
		return "W"
	case ui.KeyT:
		return "T"
	case ui.KeyB:
		return "B"
	case ui.KeyF:
		return "F"
	}
	return ""
}
