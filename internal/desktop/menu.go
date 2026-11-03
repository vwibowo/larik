package desktop

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"

	"larik/internal/status"
)

// command is an item of the menu bar: what it says, its key as an
// accelerator, and what it does, or a role of MyGo's; a zero command is a
// separator.
type command struct {
	label, key string
	run        func()
	role       mygo.MenuRole
}

// menuGroup is a menu of the menu bar, or a role's whole menu.
type menuGroup struct {
	title string
	items []command
	role  mygo.MenuRole
}

// menuBar is the macOS menu bar. AppKit gives a Command key to the menu
// bar before the window, so MyGo's default one would take Command+W to
// close the window, every tab with it, and Command+plus and minus to zoom
// a page the window doesn't have. This one gives those keys the window's
// meanings, and lists the window's other keys where they can be found.
func (a *App) menuBar() []menuGroup {
	return []menuGroup{
		{role: mygo.RoleAppMenu},
		{title: "File", items: []command{
			{label: "New Session", key: "Cmd+T", run: func() { a.newTab(a.currentDir(), "", "") }},
			{label: "New Session in Split", key: "Cmd+D", run: a.splitRight},
			{label: "Open Folder…", key: "Cmd+O", run: a.openFolder},
			{},
			{label: "Close Tab", key: "Cmd+W", run: func() {
				if t := a.focused(); t != nil {
					a.askClose([]*tab{t}, nil)
				}
			}},
			{label: "Close Window", key: "Shift+Cmd+W", role: mygo.RoleClose},
			{label: "Reopen Closed Tab", key: "Shift+Cmd+T", run: a.reopenClosed},
		}},
		{title: "Edit", items: []command{
			{role: mygo.RoleUndo}, {role: mygo.RoleRedo}, {},
			{role: mygo.RoleCut}, {role: mygo.RoleCopy}, {role: mygo.RolePaste}, {role: mygo.RoleSelectAll},
			{},
			{label: "Find…", key: "Cmd+F", run: a.openFind},
			{label: "Search Sessions…", key: "Cmd+K", run: func() { a.focusSearch, a.noSidebar = true, false }},
		}},
		{title: "View", items: []command{
			{label: "Toggle Sidebar", key: "Cmd+B", run: func() { a.noSidebar = !a.noSidebar }},
			{label: "Toggle Changes", key: "Shift+Cmd+G", run: a.toggleChanges},
			{},
			{label: "Bigger Text", key: "Cmd+=", run: func() { a.zoom(terminal.Zoom{Steps: 1}) }},
			{label: "Smaller Text", key: "Cmd+-", run: func() { a.zoom(terminal.Zoom{Steps: -1}) }},
			{label: "Actual Size", key: "Cmd+0", run: func() { a.zoom(terminal.Zoom{Reset: true}) }},
			{},
			{role: mygo.RoleToggleFullScreen},
		}},
		{title: "Window", items: []command{
			{role: mygo.RoleMinimize}, {role: mygo.RoleZoom}, {},
			{label: "Next Session Waiting", key: "Cmd+J", run: a.nextWaiting},
			{label: "Show Next Tab", key: "Shift+Cmd+]", run: func() { a.cycle(1) }},
			{label: "Show Previous Tab", key: "Shift+Cmd+[", run: func() { a.cycle(-1) }},
			{},
			{role: mygo.RoleFront},
		}},
	}
}

// dockShown is how many waiting sessions the Dock menu lists.
const dockShown = 8

// dockMenu is the Dock icon's menu: the sessions waiting for you, each
// opening it, and a new session.
func (a *App) dockMenu() []command {
	var items []command
	waiting := a.waiting()
	for _, s := range waiting[:min(len(waiting), dockShown)] {
		title := s.Title
		if title == "" {
			title = "Untitled session"
		}
		what := "finished"
		switch s.Status.State {
		case status.Blocked:
			what = "needs you"
		case status.Error:
			what = "failed"
		}
		path := s.Path
		items = append(items, command{label: title + " — " + what, run: func() { a.reveal(path) }})
	}
	if len(items) > 0 {
		items = append(items, command{})
	}
	return append(items, command{label: "New Session", run: func() { a.newTab(a.currentDir(), "", "") }})
}

// menu builds the menu bar, its items running their commands through
// update, on the window's frame.
func (a *App) menu(update func(func())) *mygo.Menu {
	var bar []*mygo.MenuItem
	for _, g := range a.menuBar() {
		if g.role != "" {
			bar = append(bar, &mygo.MenuItem{Role: g.role})
			continue
		}
		bar = append(bar, &mygo.MenuItem{Label: g.title, Submenu: menuItems(g.items, update)})
	}
	return mygo.NewMenu(bar)
}

// menuItems makes commands MyGo's menu items, running through update; a
// command run also runs after, as bringing the window forward.
func menuItems(cmds []command, update func(func()), after ...func()) []*mygo.MenuItem {
	var items []*mygo.MenuItem
	for _, cmd := range cmds {
		switch {
		case cmd.run != nil:
			run := cmd.run
			items = append(items, &mygo.MenuItem{Label: cmd.label, Accelerator: cmd.key, Click: func(*mygo.MenuItem, *mygo.Window) {
				update(run)
				for _, fn := range after {
					fn()
				}
			}})
		case cmd.role != "":
			items = append(items, &mygo.MenuItem{Label: cmd.label, Role: cmd.role, Accelerator: cmd.key})
		default:
			items = append(items, mygo.Separator())
		}
	}
	return items
}
