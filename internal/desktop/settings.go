package desktop

import (
	"fmt"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// settingsView edits the window's own preferences. The full agent config
// remains in larik's /config editor, reached from the last row.
func (a *App) settingsView(c *ui.Context) {
	t := a.palette(c.Theme())
	ui.Themed(c, t).Fill().Background(t.Background).Children(func() {
		ui.Column(c).Fill().Padding(24).Gap(18).Children(func() {
			ui.Text(c, "Larik Settings").Bold().FontSize(20)
			ui.Column(c).Gap(8).Children(func() {
				ui.Text(c, "Window").Bold().FontSize(13)
				showSidebar := !a.noSidebar
				if ui.Checkbox(c.Key("sidebar"), &showSidebar, "Show sidebar").Changed() {
					a.noSidebar = !showSidebar
					a.settingsChanged()
				}
			})
			ui.Column(c).Gap(8).Children(func() {
				ui.Text(c, "Terminal text").Bold().FontSize(13)
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					if ui.Button(c.Key("smaller"), "Smaller").Clicked() {
						a.zoom(terminal.Zoom{Steps: -1})
						a.settingsChanged()
					}
					ui.Text(c, fmt.Sprintf("%g pt", a.termFont().Size)).MinWidth(48)
					if ui.Button(c.Key("larger"), "Larger").Clicked() {
						a.zoom(terminal.Zoom{Steps: 1})
						a.settingsChanged()
					}
					if ui.Button(c.Key("reset"), "Reset").Clicked() {
						a.zoom(terminal.Zoom{Reset: true})
						a.settingsChanged()
					}
				})
			})
			ui.Box(c).Height(1).Background(t.Border)
			ui.Column(c).Gap(6).Children(func() {
				ui.Text(c, "Agent configuration").Bold().FontSize(13)
				ui.Text(c, "Models, permissions, tools, and project settings use Larik's config editor.").TextColor(t.TextMuted)
				if ui.Button(c.Key("advanced"), "Open advanced settings…").Clicked() {
					a.openAdvancedSettings()
					if a.raise != nil {
						a.raise()
					}
					a.settingsChanged()
				}
			})
		})
	})
}

func (a *App) settingsChanged() {
	a.saveState()
	if a.redraw != nil {
		a.redraw()
	}
}
