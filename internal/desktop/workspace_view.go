package desktop

import (
	"fmt"
	"slices"

	"github.com/egoist/mygo/ui"
)

type workspaceDrag struct{ w *workspace }

type workspaceRename struct {
	w         *workspace
	name, err string
	open      bool
}

func (a *App) askRenameWorkspace(w *workspace) {
	if w != nil {
		a.workspaceRename = &workspaceRename{w: w, name: w.name, open: true}
	}
}

func (a *App) workspaceRenameDialog(c *ui.Context) {
	r := a.workspaceRename
	if r == nil {
		return
	}
	ui.Modal(c.Key("rename-workspace"), &r.open, func() {
		ui.Column(c).Width(360).Gap(10).Children(func() {
			ui.Text(c, "Rename Workspace").FontSize(16).Bold()
			ui.TextInput(c.Key("name"), &r.name).Label("Workspace name").FillWidth().OnSubmit(func() { a.commitWorkspaceRename(r) })
			if r.err != "" {
				ui.Text(c, r.err).FontSize(12).TextColor(c.Theme().Danger)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("cancel"), "Cancel").Clicked() {
					a.workspaceRename = nil
				}
				if ui.Button(c.Key("rename"), "Rename").Clicked() {
					a.commitWorkspaceRename(r)
				}
			})
		})
	}).Label("Rename Workspace")
	if !r.open && a.workspaceRename == r {
		a.workspaceRename = nil
	}
}

func (a *App) commitWorkspaceRename(r *workspaceRename) {
	if a.workspaceRename != r {
		return
	}
	if err := a.renameWorkspace(r.w, r.name); err != nil {
		r.err = err.Error()
		return
	}
	a.workspaceRename = nil
}

func (a *App) workspaceMenu(m *ui.Menu, w *workspace) {
	if m.Item("New Workspace").Chosen() {
		a.newWorkspace("")
	}
	if m.Item("Rename Workspace…").Chosen() {
		a.askRenameWorkspace(w)
	}
	if len(a.workspaces) > 1 && m.Item("Close Workspace").Chosen() {
		if err := a.closeWorkspace(w); err != nil {
			a.notice = err.Error()
		}
	}
	if f := a.focused(); f != nil && w != a.activeWorkspace && m.Item("Move Focused Pane Here").Chosen() {
		_ = a.movePaneToWorkspace(f, w)
	}
}

func (a *App) workspaceTabs(c *ui.Context, t *ui.Theme) {
	a.currentWorkspace()
	width := 1240
	if a.contentSize != nil {
		width, _ = a.contentSize()
	}
	if !a.noSidebar {
		width -= int(a.sidebarW)
	}
	compact := width < 650
	tabWidth := 190
	if compact {
		tabWidth = 146
	}
	visible := max(1, min(len(a.workspaces), (width-280)/tabWidth))
	active := slices.Index(a.workspaces, a.activeWorkspace)
	start := 0
	if active >= visible {
		start = active - visible + 1
	}
	ui.Row(c.Key("workspace-rail")).Height(36).Padding(3).Radius(18).Background(t.Text.Alpha(0.055)).AlignItems(ui.Center).Children(func() {
		for _, w := range a.workspaces[start:min(start+visible, len(a.workspaces))] {
			a.workspaceTab(c, t, w, float32(tabWidth), compact)
		}
		if visible < len(a.workspaces) {
			more := ui.Row(c.Key("more-workspaces")).Width(28).Height(30).AlignItems(ui.Center).Justify(ui.Center).
				Radius(15).Label("Other workspaces").Tooltip("Other workspaces").Children(func() {
				ui.Icon(c, iconDown).FontSize(12).TextColor(t.TextMuted)
			})
			if more.Hovered() {
				more.Background(t.Text.Alpha(0.08))
			}
			more.Menu(func(m *ui.Menu) {
				for _, w := range a.workspaces {
					if m.Item(w.name).Chosen() {
						a.switchWorkspace(w)
					}
				}
			})
		}
	})
	newButton := ui.Row(c.Key("new-workspace")).Width(28).Height(30).AlignItems(ui.Center).Justify(ui.Center).
		Radius(15).Label("New workspace").Tooltip("New workspace").Children(func() {
		ui.Icon(c, iconPlus).FontSize(15).TextColor(t.TextMuted)
	})
	if newButton.Hovered() {
		newButton.Background(t.Text.Alpha(0.08))
	}
	if newButton.Clicked() {
		a.newWorkspace("")
	}
}

func (a *App) workspaceTab(c *ui.Context, t *ui.Theme, w *workspace, width float32, compact bool) {
	active := w == a.activeWorkspace
	icon := iconPanel
	if w.focus != nil && w.focus.tab != nil {
		_, icon = a.tabKind(w.focus.tab)
	}
	bg := ui.Transparent
	if active {
		bg = t.Background
		if t.Dark {
			bg = t.SurfaceHover
		}
	}
	e := ui.Row(c.Key("workspace-"+w.id)).Width(width).Height(30).Padding(2, 8).Gap(7).AlignItems(ui.Center).
		Radius(15).Background(bg).Label(w.name).Tooltip(w.name)
	if active {
		e.Border(1, t.Text.Alpha(0.18)).Shadow(0, 1, 2, 0, ui.RGBA(0, 0, 0, 0.10))
	} else if e.Hovered() {
		e.Background(t.Text.Alpha(0.065))
	}
	e.Children(func() {
		badgeColor, iconColor := t.Text.Alpha(0.07), t.TextMuted
		if active {
			badgeColor, iconColor = t.Accent.Alpha(0.16), t.Accent
		}
		ui.Row(c.Key("icon")).Width(24).Height(24).Radius(7).Background(badgeColor).
			AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			ui.Icon(c, icon).FontSize(14).TextColor(iconColor)
		})
		name := ui.Text(c, w.name).FontSize(12).SingleLine().Shrink(1)
		if active {
			name.Bold()
		} else {
			name.TextColor(t.TextMuted)
		}
		if !compact {
			ui.Box(c).Width(1).Height(13).Background(t.Text.Alpha(0.12))
			count := len(w.layout.leaves())
			unit := "pane"
			if count != 1 {
				unit = "panes"
			}
			ui.Text(c, fmt.Sprintf("%d %s", count, unit)).FontSize(11).TextColor(t.TextMuted).SingleLine()
		}
		for _, p := range w.layout.leaves() {
			if a.needsLook(p.tab) {
				ui.Box(c).Width(6).Height(6).Radius(3).Background(a.ringColor(t, p.tab))
				break
			}
		}
	})
	if e.Clicked() {
		a.switchWorkspace(w)
	}
	e.Drag(workspaceDrag{w})
	e.ContextMenu(func(m *ui.Menu) { a.workspaceMenu(m, w) })
	if d, ok := ui.Drop[workspaceDrag](e); ok && d.w != w {
		_ = a.reorderWorkspace(d.w, slices.Index(a.workspaces, w)+1)
	}
	if d, ok := ui.Drop[paneDrag](e); ok {
		_ = a.movePaneToWorkspace(d.p.tab, w)
	}
	if d, ok := ui.Drop[sessionDrag](e); ok {
		if tb := a.sessionTab(string(d)); tb != nil {
			if a.workspaceOf(tb) == w {
				a.switchWorkspace(w)
				a.activate(tb)
			} else if a.workspaceOf(tb) != nil {
				_ = a.movePaneToWorkspace(tb, w)
			} else {
				a.switchWorkspace(w)
				a.activate(tb)
			}
		}
	}
	if _, ok := ui.DragOver[paneDrag](e); ok {
		e.Background(t.Accent.Alpha(0.28))
	}
}
