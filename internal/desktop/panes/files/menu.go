package filespane

import (
	"path/filepath"
	"slices"

	"github.com/egoist/mygo/ui"
)

type MenuOptions struct {
	State     *State
	Path      string
	Dir, Root bool
	Bulk      bool
	Remote    bool
	OpenPath  func(string)
	ShowFile  func(string)
	CopyText  func(string)
	BeginEdit func(string, string)
	Duplicate func(string)
	Move      func([]string)
	Trash     func([]string, bool)
}

// Menu builds the actions for one entry. Host callbacks own dialogs and I/O.
func Menu(m *ui.Menu, v MenuOptions) {
	f, path := v.State, v.Path
	if f == nil || !WithinRoot(f.Root, path) {
		return
	}
	bulk := v.Bulk && !v.Root
	if bulk && !slices.Contains(f.Picked, path) {
		f.Picked, f.Anchor = []string{path}, path
	}
	if v.Dir {
		if m.Item("New File…").Chosen() {
			v.BeginEdit(path, "file")
		}
		if m.Item("New Folder…").Chosen() {
			v.BeginEdit(path, "folder")
		}
		m.Separator()
	}
	if !v.Remote {
		if m.Item("Open externally").Chosen() && v.OpenPath != nil {
			v.OpenPath(path)
		}
		if m.Item(RevealLabel()).Chosen() && v.ShowFile != nil {
			v.ShowFile(path)
		}
	}
	if m.Item("Copy Path").Chosen() && v.CopyText != nil {
		v.CopyText(path)
	}
	if v.Root {
		return
	}
	paths := []string{path}
	if bulk {
		paths = slices.Clone(f.Picked)
	}
	if m.Item("Copy Relative Path").Chosen() && v.CopyText != nil {
		rel, _ := filepath.Rel(f.Root, path)
		v.CopyText(rel)
	}
	m.Separator()
	if len(paths) == 1 && m.Item("Rename…").Chosen() {
		v.BeginEdit(path, "rename")
	}
	if len(paths) == 1 && m.Item("Duplicate").Chosen() {
		v.Duplicate(path)
	}
	if bulk && m.Item("Move To…").Chosen() {
		v.Move(paths)
	}
	if m.Item("Move to Trash…").Chosen() {
		v.Trash(paths, bulk)
	}
}
