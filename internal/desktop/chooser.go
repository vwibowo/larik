package desktop

import (
	"slices"

	"github.com/egoist/mygo/ui"

	"larik/internal/desktop/panes"
	chooserpane "larik/internal/desktop/panes/chooser"
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
type choice = chooserpane.Selection

func (t *tab) chooserState() *choice {
	if t == nil {
		return nil
	}
	selection, _ := t.pane.(*choice)
	return selection
}

// newChooser opens a chooser tab in dir.
func (a *App) newChooser(dir string) *tab {
	a.notice = ""
	selection := chooserpane.NewSelection()
	t := &tab{id: newTabID(), dir: dir, pane: &selection}
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
	if i < 0 || i >= len(paneVariants) {
		return nil
	}
	v := paneVariants[i]
	if v.Kind == panes.Worktree {
		a.newWorktreeSession(dir)
		return nil
	}
	return v.start(a, dir)
}

// choose starts what the chooser ch offers at i, in its place.
func (a *App) choose(ch *tab, i int) {
	if p := a.paneOf(ch); p != nil {
		a.focusPane(p)
	}
	if i >= 0 && i < len(paneVariants) && paneVariants[i].Kind == panes.Worktree {
		// Made off the main thread; it shows in the pane the chooser's in,
		// which the chooser then leaves to it.
		a.newWorktreeSessionThen(ch.dir, ch, func(t *tab) { a.replaceTab(ch, t) })
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
	return a.chooser(c, t, "New Pane", "", tb.chooserState(), tb.dir, func(dir string) { tb.dir = dir }, func(i int) { a.choose(tb, i) })
}

// chooser connects the folder and tab actions to the chooser domain view.
func (a *App) chooser(c *ui.Context, theme *ui.Theme, title, hint string, ch *choice, dir string, setDir func(string), choose func(int)) ui.Element {
	options := make([]chooserpane.Option, 0, len(paneVariants))
	for _, v := range paneVariants {
		options = append(options, chooserpane.Option{Title: v.Title, About: v.About, Key: v.key, Icon: v.icon})
	}
	opts := chooserpane.ViewOptions{Title: title, Hint: hint, DirLabel: tildePath(dir), Font: mono, Options: options, Choose: choose}
	if a.pickDir != nil || isRemote(dir) {
		opts.ChangeDir = func() {
			if isRemote(dir) {
				a.askRemoteFolder(dir, setDir)
			} else {
				a.pickDir(dir, setDir)
			}
		}
	}
	return chooserpane.View(c, theme, ch, opts)
}
