package desktop

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// A workspace owns one pane tree in a native window. The active tree is
// mirrored in App's layout fields so the existing pane operations stay local.
type workspace struct {
	id, name string
	layout   *pane
	focus    *pane
	zoomed   bool
	undo     []arrangement
}

func paneOfTree(root *pane, t *tab) *pane {
	for _, p := range root.leaves() {
		if p.tab == t {
			return p
		}
	}
	return nil
}

func (a *App) ensureWorkspace() *workspace {
	if a.activeWorkspace != nil {
		return a.activeWorkspace
	}
	w := &workspace{id: newTabID(), name: "Workspace 1"}
	a.workspaces = []*workspace{w}
	a.activeWorkspace = w
	return w
}

func (a *App) currentWorkspace() *workspace {
	w := a.ensureWorkspace()
	w.layout, w.focus, w.zoomed, w.undo = a.layout, a.focus, a.zoomed, a.undo
	return w
}

func (a *App) workspaceByID(id string) *workspace {
	for _, w := range a.workspaces {
		if w.id == id {
			return w
		}
	}
	return nil
}

func (a *App) workspaceOf(t *tab) *workspace {
	if t == nil {
		return nil
	}
	a.currentWorkspace()
	for _, w := range a.workspaces {
		for _, p := range w.layout.leaves() {
			if p.tab == t {
				return w
			}
		}
	}
	return nil
}

func (a *App) switchWorkspace(w *workspace) {
	if w == nil || w == a.ensureWorkspace() {
		return
	}
	a.currentWorkspace()
	a.activeWorkspace = w
	a.layout, a.focus, a.zoomed, a.undo = w.layout, w.focus, w.zoomed, w.undo
	if a.focus == nil && a.layout != nil {
		a.focus = a.layout.leaves()[0]
	}
	if a.focus != nil {
		a.focusPane(a.focus)
	} else {
		a.focusNext = nil
	}
}

func (a *App) newWorkspace(name string) *workspace {
	a.ensureWorkspace()
	name = strings.TrimSpace(name)
	if strings.TrimSpace(name) == "" {
		for n := 1; ; n++ {
			name = fmt.Sprintf("Workspace %d", n)
			if !a.workspaceNameUsed(name, nil) {
				break
			}
		}
	}
	w := &workspace{id: newTabID(), name: name}
	a.workspaces = append(a.workspaces, w)
	a.switchWorkspace(w)
	return w
}

func (a *App) workspaceNameUsed(name string, except *workspace) bool {
	for _, w := range a.workspaces {
		if w != except && strings.EqualFold(w.name, name) {
			return true
		}
	}
	return false
}

func (a *App) renameWorkspace(w *workspace, name string) error {
	name = strings.TrimSpace(name)
	if w == nil || len([]rune(name)) == 0 || len([]rune(name)) > 64 {
		return errors.New("workspace name must be 1–64 characters")
	}
	if a.workspaceNameUsed(name, w) {
		return errors.New("workspace name is already in use")
	}
	w.name = name
	return nil
}

func (a *App) reorderWorkspace(w *workspace, position int) error {
	if w == nil || position < 1 || position > len(a.workspaces) {
		return errors.New("workspace position is out of range")
	}
	old := slices.Index(a.workspaces, w)
	a.workspaces = slices.Delete(a.workspaces, old, old+1)
	i := position - 1
	a.workspaces = slices.Insert(a.workspaces, i, w)
	return nil
}

func (a *App) cycleWorkspace(by int) {
	if len(a.workspaces) < 2 {
		return
	}
	i := slices.Index(a.workspaces, a.activeWorkspace)
	a.switchWorkspace(a.workspaces[(i+by+len(a.workspaces))%len(a.workspaces)])
}

// removeWorkspace is called only after all of its panes have closed.
func (a *App) removeWorkspace(w *workspace) {
	if w == nil || len(a.workspaces) < 2 {
		return
	}
	i := slices.Index(a.workspaces, w)
	if i < 0 {
		return
	}
	if w == a.activeWorkspace {
		next := a.workspaces[min(i+1, len(a.workspaces)-1)]
		if next == w {
			next = a.workspaces[i-1]
		}
		a.switchWorkspace(next)
	}
	a.workspaces = slices.Delete(a.workspaces, i, i+1)
}

func (a *App) closeWorkspace(w *workspace) error {
	if a.confirming {
		return errors.New("a close confirmation is already open")
	}
	if w == nil {
		return errors.New("no such workspace")
	}
	if len(a.workspaces) == 1 {
		return errors.New("the last workspace cannot be closed")
	}
	a.currentWorkspace()
	var tabs []*tab
	for _, p := range w.layout.leaves() {
		tabs = append(tabs, p.tab)
	}
	a.closingWorkspace = w
	a.askClose(tabs, nil)
	return nil
}

// removeInactivePane removes a pane from a non-visible workspace without
// marking any of its other panes as read or changing window focus.
func (a *App) removeInactivePane(w *workspace, p *pane) {
	if w == nil || p == nil {
		return
	}
	if p.parent == nil {
		w.layout, w.focus, w.zoomed = nil, nil, false
		return
	}
	parent := p.parent
	other := parent.first
	if other == p {
		other = parent.second
	}
	other.parent = parent.parent
	if parent.parent == nil {
		w.layout = other
	} else if parent.parent.first == parent {
		parent.parent.first = other
	} else {
		parent.parent.second = other
	}
	if w.focus == p {
		w.focus = other.leaves()[0]
	}
	if w.layout == nil || !w.layout.split() {
		w.zoomed = false
	}
}

func (a *App) releaseInactivePane(t *tab) bool {
	if w := a.workspaceOf(t); w != nil && w != a.activeWorkspace {
		a.removeInactivePane(w, paneOfTree(w.layout, t))
		w.undo = nil
		a.undo = nil
		return true
	}
	return false
}

func (a *App) movePaneToWorkspace(t *tab, target *workspace) error {
	if target == nil || t == nil {
		return errors.New("no such pane or workspace")
	}
	source := a.workspaceOf(t)
	if source == nil || source == target {
		return errors.New("pane already belongs to that workspace or is not open")
	}
	for _, p := range source.layout.leaves() {
		if p.tab == t {
			if source == a.activeWorkspace {
				a.removePane(p)
			} else {
				a.removeInactivePane(source, p)
			}
			break
		}
	}
	if source == a.activeWorkspace {
		a.undo = nil
	} else {
		source.undo = nil
	}
	a.switchWorkspace(target)
	a.undo = nil
	wasRestoring := a.restoring
	a.restoring = true // an explicit move may use a smaller pane than automatic opening
	a.activate(t)
	a.restoring = wasRestoring
	return nil
}
