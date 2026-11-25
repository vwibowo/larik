package databasepane

import "github.com/egoist/mygo/ui"

type HeaderOptions struct {
	State                                               *State
	Driver, RefreshShortcut                             string
	DatabaseIcon, ReloadIcon, PanelIcon, PanelRightIcon *ui.SVG
	IconButton                                          IconButton
	Refresh, Add                                        func()
}

type EmptyOptions struct {
	State        *State
	Names        []string
	DatabaseIcon *ui.SVG
	Refresh, Add func()
}

func Header(c *ui.Context, theme *ui.Theme, opts HeaderOptions) {
	d := opts.State
	ui.Row(c).Height(48).Gap(8).Padding(7, 14).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
		ui.Icon(c, opts.DatabaseIcon).FontSize(17).TextColor(theme.Accent)
		name := "Database Manager"
		if d.Profile != "" {
			name = d.Profile
		}
		ui.Text(c, name).FontSize(14).Bold().Grow(1).Shrink(1).SingleLine()
		if d.Editing {
			return
		}
		if opts.Driver != "" {
			driver := DriverLabel(opts.Driver)
			ui.Text(c, driver+" · Read only").FontSize(11).TextColor(theme.TextMuted).SingleLine()
		}
		if d.Profile != "" {
			opts.IconButton("db-refresh", opts.ReloadIcon, "Refresh database objects ("+opts.RefreshShortcut+")", false, func() {
				if !d.Loading {
					opts.Refresh()
				}
			})
			if !d.Compact {
				browserTip := "Show tables on the left"
				if d.ShowBrowser {
					browserTip = "Hide tables on the left"
				}
				opts.IconButton("db-objects-toggle", opts.PanelIcon, browserTip, d.ShowBrowser, func() {
					d.ShowBrowser = !d.ShowBrowser
				})
				if d.HasSelectedRow() {
					detailsTip := "Show details on the right"
					if d.ShowDetails {
						detailsTip = "Hide details on the right"
					}
					opts.IconButton("db-details-toggle", opts.PanelRightIcon, detailsTip, d.ShowDetails, func() {
						d.ShowDetails = !d.ShowDetails
					})
				} else {
					const tip = "Select a row to show details on the right"
					ui.Box(c.Key("db-details-toggle")).Padding(6).Radius(6).Label(tip).Tooltip(tip).Disabled(true).Children(func() {
						ui.Icon(c, opts.PanelRightIcon).FontSize(15).TextColor(theme.TextMuted)
					})
				}
			}
		} else if ui.Button(c.Key("db-add"), "Add connection").Clicked() {
			opts.Add()
		}
	})
}

func Empty(c *ui.Context, theme *ui.Theme, opts EmptyOptions) {
	names := opts.Names
	ui.Column(c).Grow(1).Center().Gap(10).Children(func() {
		ui.Icon(c, opts.DatabaseIcon).FontSize(34).TextColor(theme.TextMuted)
		ui.Text(c, "Choose a database connection").FontSize(16).Bold()
		ui.Text(c, "Saved connections are in the Databases sidebar section.").FontSize(12).TextColor(theme.TextMuted)
		if len(names) > 0 {
			if opts.State.EmptyChoice == "" {
				opts.State.EmptyChoice = names[0]
			}
			ui.Select(c.Key("db-empty-choice"), &opts.State.EmptyChoice, names).Width(250).Label("Database connection")
			if ui.Button(c.Key("db-open-choice"), "Open connection").Clicked() {
				opts.State.Profile = opts.State.EmptyChoice
				opts.Refresh()
			}
		} else if ui.Button(c.Key("db-empty-add"), "Add connection").Clicked() {
			opts.Add()
		}
	})
}
