package databasepane

import (
	"github.com/egoist/mygo/ui"
	"larik/internal/desktop/panes"
)

func View(c *ui.Context, theme *ui.Theme, v panes.DatabaseView) ui.Element {
	pane := ui.Column(c.Key(v.ID + "-database")).Grow(1).MinWidth(0).Focusable()
	if bounds := pane.Bounds(); bounds.W > 0 {
		*v.Narrow, *v.Compact = bounds.W < 600, bounds.W < 980
		if *v.LegacyEditorPercent > 0 && bounds.H > 0 {
			if *v.LegacyEditorPercent == 34 {
				*v.EditorHeight = 180
			} else {
				*v.EditorHeight = max(120, min(bounds.H**v.LegacyEditorPercent/100, 320))
			}
			*v.LegacyEditorPercent = 0
		}
		if bounds.H > 0 {
			*v.EditorHeight = max(120, min(*v.EditorHeight, bounds.H*0.70))
		}
	}
	return pane.Children(func() {
		v.Header()
		if v.Editing() {
			v.Form()
			return
		}
		if err := v.StoreError(); err != nil {
			ui.Text(c, "Cannot load saved connections: "+err.Error()).Padding(16).TextColor(theme.Danger)
			return
		}
		profile := v.Profile()
		if profile == "" {
			v.Empty()
			return
		}
		if !v.ProfileExists(profile) {
			ui.Text(c, "Connection removed. Choose another from the sidebar.").Padding(16)
			return
		}
		if *v.Compact {
			ui.Tabs(c.Key("db-regions"), v.Region, "Tables", "Results", "Details").Label("Database region")
			switch *v.Region {
			case panes.DatabaseTablesRegion:
				v.Objects()
			case panes.DatabaseDetailsRegion:
				v.Inspector()
			default:
				v.Center()
			}
			return
		}
		main := v.Center
		if v.ShowBrowser() {
			center := main
			main = func() { ui.SplitPercent(c, v.BrowserWidth, v.Objects, center).Grow(1) }
		}
		if v.ShowDetails() && v.HasSelectedRow() {
			ui.SplitPercent(c, v.InspectorWidth, main, v.Inspector).Grow(1)
		} else {
			main()
		}
	})
}
