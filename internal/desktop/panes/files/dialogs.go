package filespane

import (
	"fmt"
	"path/filepath"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

type EditDialogOptions struct {
	Open   *bool
	Path   string
	Kind   string
	Name   *string
	Error  string
	Busy   bool
	Focus  *bool
	Commit func()
	Cancel func()
}

func EditDialog(c *ui.Context, v EditDialogOptions) {
	title := "Rename"
	switch v.Kind {
	case "file":
		title = "New File"
	case "folder":
		title = "New Folder"
	}
	ui.Modal(c.Key("files-edit"), v.Open, func() {
		ui.Column(c).Width(340).Gap(10).Children(func() {
			ui.Text(c, title).FontSize(16).Bold()
			ui.Text(c, filepath.Base(v.Path)).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine()
			field := ui.TextInput(c.Key("files-name"), v.Name).Label("Name").Placeholder("Name").FillWidth().OnSubmit(v.Commit)
			if *v.Focus {
				field.Focus().SetTextSelection(0, utf8.RuneCountInString(*v.Name))
				*v.Focus = false
			}
			if v.Error != "" {
				ui.Text(c, v.Error).FontSize(12).TextColor(c.Theme().Danger)
			}
			ui.Row(c).Gap(8).Justify(ui.End).AlignItems(ui.Center).Children(func() {
				if ui.Button(c.Key("cancel"), "Cancel").Clicked() {
					v.Cancel()
				}
				if ui.Button(c.Key("commit"), title).Disabled(v.Busy).Clicked() {
					v.Commit()
				}
			})
		})
	}).Label(title)
}

type MoveDialogOptions struct {
	Open    *bool
	Count   int
	Dest    *string
	State   *State
	LoadDir func(string)
	Move    func()
	Cancel  func()
}

func MoveDialog(c *ui.Context, v MoveDialogOptions) {
	ui.Modal(c.Key("files-move"), v.Open, func() {
		ui.Column(c).Width(410).Gap(10).Children(func() {
			ui.Text(c, fmt.Sprintf("Move %d item(s) to…", v.Count)).FontSize(16).Bold()
			ui.Text(c, "Choose a folder in this explorer").FontSize(12).TextColor(c.Theme().TextMuted)
			ui.Scroll(c).Height(300).Children(func() {
				ui.Tree(c, func() { moveFolders(c, v, v.State.Root, 0) })
			})
			ui.Text(c, *v.Dest).FontSize(11).SingleLine().Tooltip(*v.Dest)
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					v.Cancel()
				}
				if ui.Button(c, "Move").Clicked() {
					v.Move()
				}
			})
		})
	}).Label("Move files")
}

func moveFolders(c *ui.Context, v MoveDialogOptions, dir string, depth int) {
	if depth > 80 {
		return
	}
	f := v.State
	v.LoadDir(dir)
	open := f.Open[dir]
	if open == nil {
		value := dir == f.Root
		open = &value
		f.Open[dir] = open
	}
	item := ui.TreeItem(c.Key("move-"+dir), filepath.Base(dir), open, func() {
		if *open {
			if entries := f.Dirs[dir]; entries != nil && entries.Loaded {
				for _, e := range entries.Entries {
					if e.Dir {
						moveFolders(c, v, e.Path, depth+1)
					}
				}
			}
		}
	}).Selected(*v.Dest == dir)
	if item.Clicked() {
		*v.Dest = dir
		*open = true
	}
}

type ConflictDialogOptions struct {
	Open   *bool
	Dest   string
	Choose func(string)
}

func ConflictDialog(c *ui.Context, v ConflictDialogOptions) {
	ui.Modal(c.Key("files-conflict"), v.Open, func() {
		ui.Column(c).Width(410).Gap(10).Children(func() {
			ui.Text(c, "An item already exists").FontSize(16).Bold()
			ui.Text(c, fmt.Sprintf("“%s” already exists in %s. Replace moves the existing item to Trash.", filepath.Base(v.Dest), filepath.Dir(v.Dest))).FontSize(12)
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Skip").Clicked() {
					v.Choose("skip")
				}
				if ui.Button(c, "Keep Both").Clicked() {
					v.Choose("both")
				}
				if ui.Button(c, "Replace").Clicked() {
					v.Choose("replace")
				}
			})
		})
	}).Label("File name conflict")
}
