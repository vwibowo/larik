package filespane

import (
	"runtime"
	"slices"

	"github.com/egoist/mygo/ui"
)

// SelectionChange is the file the window should preview after a selection action.
type SelectionChange struct {
	Path  string
	Dir   bool
	Apply bool
}

func (f *State) PickedPath(path string) bool {
	return slices.Contains(f.Picked, path) || len(f.Picked) == 0 && f.Selected == path
}

func SelectionModifiers(mods ui.Modifiers) (toggle, extend bool) {
	add := ui.Ctrl
	if runtime.GOOS == "darwin" {
		add = ui.Super
	}
	return mods&add != 0, mods&ui.Shift != 0
}

func (f *State) ClickPath(path string, dir bool, mods ui.Modifiers, bulk bool) SelectionChange {
	if !bulk {
		return SelectionChange{Path: path, Dir: dir, Apply: true}
	}
	toggle, extend := SelectionModifiers(mods)
	switch {
	case extend:
		rows := f.LastRows
		if len(rows) == 0 {
			rows = f.Rows
		}
		from, to := slices.Index(rows, f.Anchor), slices.Index(rows, path)
		if from < 0 {
			from = to
		}
		if from > to {
			from, to = to, from
		}
		if from >= 0 && to >= 0 {
			f.Picked = slices.Clone(rows[from : to+1])
		}
	case toggle:
		if i := slices.Index(f.Picked, path); i >= 0 {
			f.Picked = slices.Delete(f.Picked, i, i+1)
		} else {
			f.Picked = append(f.Picked, path)
		}
		f.Anchor = path
	default:
		f.Picked, f.Anchor = []string{path}, path
	}
	if slices.Contains(f.Picked, path) {
		return SelectionChange{Path: path, Dir: dir, Apply: true}
	}
	if len(f.Picked) == 0 {
		return SelectionChange{Apply: true}
	}
	if f.Selected == path {
		remaining := f.Picked[len(f.Picked)-1]
		return SelectionChange{Path: remaining, Dir: IsDirectory(remaining), Apply: true}
	}
	return SelectionChange{}
}

func (f *State) RightClickPath(path string, dir, bulk bool) SelectionChange {
	if !bulk || slices.Contains(f.Picked, path) {
		return SelectionChange{}
	}
	f.Picked, f.Anchor = []string{path}, path
	return SelectionChange{Path: path, Dir: dir, Apply: true}
}

func (f *State) DragPaths(path string) []string {
	if slices.Contains(f.Picked, path) {
		return slices.Clone(f.Picked)
	}
	return []string{path}
}
