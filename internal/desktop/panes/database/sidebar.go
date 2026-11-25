package databasepane

import "github.com/egoist/mygo/ui"

type SidebarOptions struct {
	Names                  []string
	Driver                 func(string) string
	DatabaseIcon, PlusIcon *ui.SVG
	Open, Edit, Delete     func(string)
	Add                    func()
}

func Sidebar(c *ui.Context, theme *ui.Theme, opts SidebarOptions) {
	ui.SidebarSection(c.Key("desktop-databases"), "Databases", nil, func() {
		for _, name := range opts.Names {
			driver := opts.Driver(name)
			item := ui.SidebarItem(c, "database:"+name, opts.DatabaseIcon, name).Tooltip(DriverLabel(driver) + " · " + name)
			item.Children(func() {
				ui.Text(c, DriverLabel(driver)).FontSize(10).TextColor(theme.TextMuted).SingleLine()
			})
			if item.Clicked() {
				opts.Open(name)
			}
			item.ContextMenu(func(m *ui.Menu) {
				if m.Item("Open").Chosen() {
					opts.Open(name)
				}
				if m.Item("Edit Connection").Chosen() {
					opts.Edit(name)
				}
				if m.Item("Delete Connection").Chosen() {
					opts.Delete(name)
				}
			})
		}
		row := ui.ButtonBase(c.Key("db-sidebar-add")).Gap(6).Padding(4, 12, 4, 14).Radius(6).AlignItems(ui.Center).Cursor(ui.CursorPointer).Label("Add database connection").Children(func() {
			ui.Icon(c, opts.PlusIcon).FontSize(12).TextColor(theme.TextMuted)
			ui.Text(c, "Add connection").FontSize(12).TextColor(theme.TextMuted)
		})
		if row.Clicked() {
			opts.Add()
		}
	})
}
