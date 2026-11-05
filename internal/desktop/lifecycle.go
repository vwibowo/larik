package desktop

import (
	"fmt"
	"slices"

	"github.com/egoist/mygo/ui"

	"larik/internal/session"
)

// Archiving and the Trash: a session archived leaves the sidebar, the
// palette and /resume for its project's Archived list, which opening it
// from brings it back; one moved to the Trash goes there whole, its
// transcript and what larik kept beside it in a folder named for it, and
// can be got back from there until the Trash is emptied. Neither touches
// a session a process has open: its tab, or another terminal, ends it
// first. Hiding a session, as earlier versions did, only hides it.

// unmovable says why the session at path can't be archived or trashed
// now, or "".
func (a *App) unmovable(path string) string {
	s, _, ok := a.findAny(path)
	switch {
	case !ok:
		return "That session isn't there any more."
	case s.Running && a.tabOf(path) != nil:
		return fmt.Sprintf("“%s” is open in a pane: close the pane first.", s.Title)
	case s.Running:
		return fmt.Sprintf("“%s” is open in another terminal: end it there first.", s.Title)
	}
	return ""
}

// archive archives the session at path, reporting whether it did; the
// notice says why not.
func (a *App) archive(path string) bool {
	if msg := a.unmovable(path); msg != "" {
		a.notice = msg
		return false
	}
	to, err := session.Archive(path)
	if err != nil {
		a.notice = "Can't archive the session: " + err.Error()
		return false
	}
	a.forget(path)
	a.moved(path, to, true)
	return true
}

// unarchive brings the archived session at path back among its
// project's, and returns where it is now, or "".
func (a *App) unarchive(path string) string {
	to, err := session.Unarchive(path)
	if err != nil {
		a.notice = "Can't unarchive the session: " + err.Error()
		return ""
	}
	a.moved(path, to, false)
	return to
}

// trash moves the session at path to the Trash, after asking.
func (a *App) trash(path string) {
	if msg := a.unmovable(path); msg != "" {
		a.notice = msg
		return
	}
	s, _, _ := a.findAny(path)
	a.confirmAsk(fmt.Sprintf("Move “%s” to the Trash?", s.Title),
		"Its conversation, and what larik kept beside it, go to the Trash in a folder of their own, where you can get them back until it's emptied.",
		"Move to Trash", func() { a.trashNow(path) })
}

// trashNow moves the session at path to the Trash, off the main thread,
// as the Trash may take a while.
func (a *App) trashNow(path string) {
	if a.trashItem == nil {
		a.notice = "There's no Trash to move it to here."
		return
	}
	folder, err := session.Gather(path)
	if err != nil {
		a.notice = "Can't move the session to the Trash: " + err.Error()
		return
	}
	a.forget(path)
	a.moved(path, "", false)
	go func() {
		err := a.trashItem(folder)
		if err != nil {
			err = fmt.Errorf("%w; it's where it was", err)
			if uerr := session.Ungather(path, folder); uerr != nil {
				err = fmt.Errorf("%v, and it stayed in %s: %v", err, folder, uerr)
			}
		}
		a.update(func() {
			if err != nil {
				a.notice = "Can't move the session to the Trash: " + err.Error()
			}
			a.refreshIndex()
		})
	}()
}

// forget lets go of what the window keeps of the session at path, as it
// leaves: its tab, whose larik ended, its pin and its hiding.
func (a *App) forget(path string) {
	if t := a.tabOf(path); t != nil {
		a.closeTab(t)
	}
	a.pin(path, false)
	delete(a.hidden, path)
}

// moved moves the session at from in the index as it moved on disk, to
// to, archived or not, or out of it for "", until the index is read
// again.
func (a *App) moved(from, to string, archived bool) {
	for i := range a.projects {
		p := &a.projects[i]
		for _, list := range []*[]session.Indexed{&p.Sessions, &p.Archived} {
			j := slices.IndexFunc(*list, func(s session.Indexed) bool { return s.Path == from })
			if j < 0 {
				continue
			}
			s := (*list)[j]
			*list = slices.Delete(*list, j, j+1)
			if to == "" {
				break
			}
			s.Path = to
			if archived {
				p.Archived = append([]session.Indexed{s}, p.Archived...)
			} else {
				p.Sessions = append([]session.Indexed{s}, p.Sessions...)
				slices.SortFunc(p.Sessions, func(x, y session.Indexed) int { return y.Modified.Compare(x.Modified) })
			}
			a.prompts[to] = a.prompts[from]
		}
	}
	a.refreshIndex()
}

// findAny is find, archived sessions too.
func (a *App) findAny(path string) (session.Indexed, session.Project, bool) {
	if s, p, ok := a.find(path); ok {
		return s, p, true
	}
	for _, p := range a.projects {
		for _, s := range p.Archived {
			if s.Path == path {
				return s, p, true
			}
		}
	}
	return session.Indexed{}, session.Project{}, false
}

// openArchived brings an archived session back and opens it.
func (a *App) openArchived(path string) {
	if to := a.unarchive(path); to != "" {
		a.selectSession(to)
	}
}

// archived lists a project's archived sessions, under its others, once
// asked for: opening one brings it back.
func (a *App) archived(c *ui.Context, t *ui.Theme, p session.Project) {
	if len(p.Archived) == 0 {
		return
	}
	if !a.showArchived[p.Root] {
		if a.footLink(c, t, "archived", fmt.Sprintf("Show %d archived", len(p.Archived))) {
			a.showArchived[p.Root] = true
		}
		return
	}
	for _, s := range p.Archived {
		label := s.Title
		if label == "" {
			label = "Untitled session"
		}
		item := ui.SidebarItem(c, s.Path, iconChat, label).Opacity(0.55).
			Tooltip(label + " (archived: opening it brings it back)")
		item.ContextMenu(func(m *ui.Menu) { a.archivedMenu(m, s) })
	}
	if a.footLink(c, t, "archived", "Leave archived ones out") {
		a.showArchived[p.Root] = false
	}
}

// archivedMenu is what an archived session's context menu offers.
func (a *App) archivedMenu(m *ui.Menu, s session.Indexed) {
	if m.Item("Open").Chosen() {
		a.openArchived(s.Path)
	}
	if m.Item("Unarchive").Chosen() {
		a.unarchive(s.Path)
	}
	if m.Item("Move to Trash…").Chosen() {
		a.trash(s.Path)
	}
	m.Separator()
	if m.Item("Copy Session ID").Chosen() && a.copyText != nil {
		a.copyText(s.ID)
	}
}
