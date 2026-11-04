package desktop

import "github.com/egoist/mygo/ui"

type layoutPick struct {
	root, name, err string
	open, focus     bool
}

func (a *App) askSaveLayout(root string) {
	a.layoutPick = &layoutPick{root: root, open: true, focus: true}
}

func (a *App) layoutDialog(c *ui.Context) {
	p := a.layoutPick
	if p == nil {
		return
	}
	ui.Modal(c.Key("save-layout"), &p.open, func() {
		ui.Column(c).Width(360).Gap(10).Children(func() {
			ui.Text(c, "Save Project Layout").FontSize(16).Bold()
			ui.TextInput(c.Key("name"), &p.name).Label("Layout name").Placeholder("Review setup").FillWidth().OnSubmit(func() { a.commitLayout(p) })
			if p.err != "" {
				ui.Text(c, p.err).FontSize(12).TextColor(c.Theme().Danger)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("cancel"), "Cancel").Clicked() {
					a.layoutPick = nil
				}
				if ui.Button(c.Key("save"), "Save").Clicked() {
					a.commitLayout(p)
				}
			})
		})
	}).Label("Save Project Layout")
	if !p.open && a.layoutPick == p {
		a.layoutPick = nil
	}
}

func (a *App) commitLayout(p *layoutPick) {
	if a.layoutPick != p {
		return
	}
	if err := a.saveProjectLayout(p.root, p.name); err != nil {
		p.err = err.Error()
		return
	}
	a.layoutPick = nil
	a.notice = "Saved layout “" + p.name + "”."
}
