package desktop

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"

	"larik/internal/status"
)

// command is an item of the menu bar: what it says, its key as an
// accelerator, and what it does, or a role of MyGo's; a zero command is a
// separator. A checked command shows a checkmark.
type command struct {
	label, key string
	run        func()
	role       mygo.MenuRole
	checked    bool
}

// The guides the Help menu opens.
const (
	helpURL  = "https://github.com/vwibowo/larik#readme"
	guideURL = "https://github.com/vwibowo/larik/blob/main/docs/desktop.md"
)

// menuGroup is a menu of the menu bar, or a role's whole menu; a role
// with items is a menu of the role's, as Help, with them in it.
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
func (a *App) menuBar() []menuGroup { return a.bindKeys(a.menuDefaults()) }

// menuDefaults is the menu bar with its commands' own keys, before those
// Settings › Keyboard gave them (see keys.go).
func (a *App) menuDefaults() []menuGroup {
	return []menuGroup{
		// MyGo's app menu, with Settings where Mac apps have it.
		{role: mygo.RoleAppMenu, items: append([]command{
			{label: "About Larik", run: a.showAbout},
			{},
			{label: "Settings…", key: "Cmd+,", run: a.openSettings},
			{},
			{role: mygo.RoleServices},
			{},
			{role: mygo.RoleHide},
			{role: mygo.RoleHideOthers},
			{role: mygo.RoleUnhide},
			{},
			{role: mygo.RoleQuit},
		}, a.quitEndingCommand()...)},
		{title: "File", items: []command{
			{label: "New Tab", key: "Cmd+T", run: a.openNew},
			{label: "New Tab in Split", key: "Cmd+D", run: a.splitRight},
			{label: "New Tab in Split Below", key: splitBelowKey(), run: func() { a.splitNew(true) }},
			{label: "New Larik Session", run: func() { a.newTab(a.currentDir(), "", "") }},
			{label: "New Session in Worktree", key: "Alt+Cmd+T", run: func() { a.newWorktreeSession(a.currentDir()) }},
			{label: "New Terminal", key: "Shift+Cmd+N", run: a.openShell},
			{label: "New Browser Tab", key: "Alt+Cmd+N", run: func() { a.openBrowser("") }},
			{label: "Open Location…", key: "Cmd+L", run: a.focusAddress},
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
			{label: paletteLabel, key: "Shift+Cmd+P", run: a.openPalette},
			{},
			{label: "Toggle Sidebar", key: "Cmd+B", run: func() { a.noSidebar = !a.noSidebar }},
			{label: "Toggle Changes", key: "Shift+Cmd+G", run: a.toggleChanges},
			{},
			{label: "Bigger Text", key: "Cmd+=", run: func() { a.zoom(terminal.Zoom{Steps: 1}) }},
			{label: "Smaller Text", key: "Cmd+-", run: func() { a.zoom(terminal.Zoom{Steps: -1}) }},
			{label: "Actual Size", key: "Cmd+0", run: func() { a.zoom(terminal.Zoom{Reset: true}) }},
			{},
			{label: "Reload Page", key: "Cmd+R", run: func() { a.browse((*mygo.Page).Reload) }},
			{label: "Back", key: "Cmd+[", run: func() { a.browse((*mygo.Page).GoBack) }},
			{label: "Forward", key: "Cmd+]", run: func() { a.browse((*mygo.Page).GoForward) }},
			{},
			{role: mygo.RoleToggleFullScreen},
		}},
		{title: "Window", items: append([]command{
			{role: mygo.RoleMinimize}, {role: mygo.RoleZoom}, {},
			{label: "Next Session Waiting", key: "Cmd+J", run: a.nextWaiting},
			{label: "Latest Notification", key: "Shift+Cmd+U", run: a.latestUnread},
			{label: "Show Next Tab", key: "Shift+Cmd+]", run: func() { a.cycle(1) }},
			{label: "Show Previous Tab", key: "Shift+Cmd+[", run: func() { a.cycle(-1) }},
			{},
			{label: "Zoom Pane", key: "Shift+Cmd+Enter", run: a.toggleZoom, checked: a.zoomed},
			{label: "Close Pane", run: func() { a.closePane(a.focus) }},
			{label: "Pane to the Left", key: "Alt+Cmd+Left", run: func() { a.focusToward(-1, 0) }},
			{label: "Pane to the Right", key: "Alt+Cmd+Right", run: func() { a.focusToward(1, 0) }},
			{label: "Pane Above", key: "Alt+Cmd+Up", run: func() { a.focusToward(0, -1) }},
			{label: "Pane Below", key: "Alt+Cmd+Down", run: func() { a.focusToward(0, 1) }},
			{},
		}, append(a.tilingCommands(), append([]command{
			{},
			{role: mygo.RoleFront},
		}, a.tabCommands()...)...)...)},
		{title: "Help", role: mygo.RoleHelp, items: []command{
			{label: "Larik Help", run: func() { a.openURL(helpURL) }},
			{label: "Desktop App Guide", run: func() { a.openURL(guideURL) }},
		}},
	}
}

// tabCommands list the open tabs at the end of the Window menu, as
// Safari and Terminal do, the first nine on Command and their number,
// the focused one checked.
func (a *App) tabCommands() []command {
	if len(a.tabs) == 0 {
		return nil
	}
	cmds := []command{{}}
	for i, tb := range a.tabs {
		cmd := command{label: a.tabTitle(tb), run: func() { a.activate(tb) }, checked: tb == a.focused()}
		if i < 9 {
			cmd.key = fmt.Sprintf("Cmd+%d", i+1)
		}
		cmds = append(cmds, cmd)
	}
	return cmds
}

// menuTabs is what the Window menu's tabs say, to set the menu bar again
// when it changes.
func (a *App) menuTabs() string {
	var b strings.Builder
	if a.zoomed {
		b.WriteString("zoomed\n") // Zoom Pane's check
	}
	for _, tb := range a.tabs {
		fmt.Fprintf(&b, "%v %s\n", tb == a.focused(), a.tabTitle(tb))
	}
	return b.String()
}

// aboutPanel is what About Larik shows: AppKit's own panel, which run
// from a plain executable rather than an app bundle has no version to
// show unless it is told one.
func (a *App) aboutPanel() mygo.AboutPanelOptions {
	return mygo.AboutPanelOptions{
		ApplicationName:    "Larik",
		ApplicationVersion: a.opts.Version,
		Credits:            "A coding agent for the terminal, in a window of terminal tabs.",
		Copyright:          "Copyright © 2026 Vinensius Wibowo. MIT License.",
	}
}

func (a *App) showAbout() {
	if a.about != nil {
		a.about(a.aboutPanel())
	}
}

func (a *App) openURL(url string) {
	if a.openExternal != nil {
		a.openExternal(url)
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
		if g.role != "" && g.items == nil {
			bar = append(bar, &mygo.MenuItem{Role: g.role})
			continue
		}
		bar = append(bar, &mygo.MenuItem{Label: g.title, Role: g.role, Submenu: menuItems(g.items, update)})
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
			item := &mygo.MenuItem{Label: cmd.label, Accelerator: cmd.key, Click: func(*mygo.MenuItem, *mygo.Window) {
				update(run)
				for _, fn := range after {
					fn()
				}
			}}
			if cmd.checked {
				item.Type, item.Checked = mygo.MenuItemCheckbox, true
			}
			items = append(items, item)
		case cmd.role != "":
			items = append(items, &mygo.MenuItem{Label: cmd.label, Role: cmd.role, Accelerator: cmd.key})
		default:
			items = append(items, mygo.Separator())
		}
	}
	return items
}
