package filespane

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/egoist/mygo/ui"
)

type TreeViewOptions struct {
	State                              *State
	RootLabel, RefreshShortcut         string
	FolderIcon, ReloadIcon, FilterIcon *ui.SVG
	Bulk, CanBrowse                    bool
	Menu                               func(*ui.Menu, string, bool, bool)
	DropTarget                         func(ui.Element, string, float32)
	SmallButton                        func(string, *ui.SVG, string, func())
	IconButton                         func(string, *ui.SVG, string, bool, func())
	BrowseRoot, Refresh, Search        func()
	LoadDir                            func(string)
	SelectNone                         func()
	Trash                              func([]string)
	ClickFile                          func(string, bool, ui.Modifiers)
	RightClick                         func(string, bool)
	DragItem                           func(ui.Element, Entry)
}

func TreeView(c *ui.Context, theme *ui.Theme, v TreeViewOptions) {
	f := v.State
	ui.Column(c).Fill().MinWidth(0).Children(func() {
		ui.Row(c).Height(40).Gap(6).Padding(0, 8, 0, 12).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
			ui.Icon(c, v.FolderIcon).FontSize(14).TextColor(theme.Accent)
			rootLabel := ui.Text(c, v.RootLabel).FontSize(12).Bold().SingleLine().Grow(1).Shrink(1).Tooltip(f.Root).Label("Explorer folder " + filepath.Base(f.Root))
			rootLabel.ContextMenu(func(m *ui.Menu) { v.Menu(m, f.Root, true, true) })
			if v.Bulk {
				v.DropTarget(rootLabel, f.Root, 0.2)
			}
			if v.CanBrowse {
				v.SmallButton("browse-files-root", v.FolderIcon, "Browse folder", v.BrowseRoot)
			}
			v.IconButton("refresh-files", v.ReloadIcon, "Refresh files ("+v.RefreshShortcut+")", false, func() { v.Refresh() })
			ui.Box(c.Key("file-visibility")).Padding(6).Radius(6).Label("File visibility").Tooltip("File visibility").Children(func() {
				ui.Icon(c, v.FilterIcon).FontSize(15).TextColor(theme.Text.Alpha(0.75))
			}).Menu(func(m *ui.Menu) {
				if m.Item("Show hidden files").Checked(f.ShowHidden).Chosen() {
					f.ShowHidden = !f.ShowHidden
					v.Refresh()
				}
				if m.Item("Show ignored files").Checked(f.ShowIgnored).Chosen() {
					f.ShowIgnored = !f.ShowIgnored
					v.Refresh()
				}
			})
		})
		ui.Row(c).Padding(7, 9).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
			field := ui.SearchField(c.Key("file-search"), &f.Query).Placeholder("Find files").Label("Search files and folders").Grow(1).MinWidth(0).
				OnChange(func() { v.Search() }).
				OnShortcut(0, ui.KeyEscape, func() { f.Query = ""; v.Search() })
			if f.FocusSearch {
				field.Focus()
				f.FocusSearch = false
			}
		})
		if f.HasQuery() {
			status := fmt.Sprintf("%d matches", f.Search.Matches)
			switch {
			case f.Search.Loading:
				status = "Searching…"
			case f.Search.Err != "":
				status = f.Search.Err
			case f.Search.More:
				status = fmt.Sprintf("First %d matches · refine your search", MaxFileMatches)
			case f.Search.Matches == 0:
				status = "No matching files or folders"
			}
			ui.Text(c, status).FontSize(11).TextColor(theme.TextMuted).Padding(7, 12)
		}
		scroll := ui.Scroll(c.Key("files-scroll")).Grow(1).Label("Files tree area " + filepath.Base(f.Root))
		if v.Bulk {
			v.DropTarget(scroll, f.Root, 0.12)
		}
		scroll.Children(func() {
			if !f.HasQuery() {
				v.LoadDir(f.Root)
			}
			f.Rows = f.Rows[:0]
			tree := ui.Tree(c.Key("files-tree"), func() { treeChildren(c, v, f.Root, 0) }).Padding(6, 8)
			if v.Bulk {
				tree.OnShortcut(ui.Super, ui.KeyA, func() {
					f.Picked = slices.Clone(f.Rows)
					if len(f.Picked) > 0 {
						f.Anchor = f.Picked[0]
					}
				}).
					OnShortcut(0, ui.KeyEscape, func() { f.Picked = nil; v.SelectNone() }).
					OnShortcut(ui.Super, ui.KeyBackspace, func() { v.Trash(slices.Clone(f.Picked)) })
			}
			f.LastRows = slices.Clone(f.Rows)
		})
	})
}

func treeChildren(c *ui.Context, v TreeViewOptions, dir string, depth int) {
	if depth > 80 { // avoid building an unbounded widget tree
		return
	}
	f := v.State
	var entries []Entry
	if f.HasQuery() {
		entries = f.Search.Tree[dir]
	} else {
		d := f.Dirs[dir]
		if d == nil || !d.Loaded {
			ui.TreeItem(c, "Loading…", nil, nil).Disabled(true)
			return
		}
		if d.Err != "" {
			ui.TreeItem(c, "Can't open folder: "+d.Err, nil, nil).Disabled(true)
			return
		}
		entries = d.Entries
		if len(entries) == 0 && dir == f.Root {
			ui.TreeItem(c, "This folder is empty", nil, nil).Disabled(true)
		}
	}
	for _, entry := range entries {
		e := entry
		f.Rows = append(f.Rows, e.Path)
		if e.Dir {
			open := f.Open[e.Path]
			if open == nil {
				initial := f.HasQuery()
				open = &initial
				f.Open[e.Path] = open
			}
			item := ui.TreeItem(c.Key(e.Path), filepath.Base(e.Path), open, func() {
				if *open && !f.HasQuery() {
					v.LoadDir(e.Path)
				}
				treeChildren(c, v, e.Path, depth+1)
			}).Selected(f.PickedPath(e.Path))
			if item.Clicked() {
				toggle, extend := SelectionModifiers(item.ClickModifiers())
				if !v.Bulk || !toggle && !extend {
					*open = true
					if !f.HasQuery() {
						v.LoadDir(e.Path)
					}
				}
				v.ClickFile(e.Path, true, item.ClickModifiers())
			}
			if item.RightClicked() {
				v.RightClick(e.Path, true)
			}
			item.ContextMenu(func(m *ui.Menu) { v.Menu(m, e.Path, true, false) })
			v.DragItem(item, e)
			if v.Bulk {
				v.DropTarget(item, e.Path, 0.2)
			}
		} else {
			item := ui.TreeItem(c.Key(e.Path), filepath.Base(e.Path), nil, nil).Selected(f.PickedPath(e.Path))
			if item.Clicked() {
				v.ClickFile(e.Path, false, item.ClickModifiers())
			}
			if item.RightClicked() {
				v.RightClick(e.Path, false)
			}
			item.ContextMenu(func(m *ui.Menu) { v.Menu(m, e.Path, false, false) })
			v.DragItem(item, e)
		}
	}
}
