package desktop

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	filespane "larik/internal/desktop/panes/files"
)

const (
	maxFilePreview  = filespane.MaxFilePreview
	maxImagePreview = filespane.MaxImagePreview
	maxFileMatches  = filespane.MaxFileMatches
)

// filesState belongs to one Files tab. The background readers only return
// immutable results; the window's update callback applies them on its frame.
func (t *tab) filesState() *filesState {
	if t == nil {
		return nil
	}
	f, _ := t.pane.(*filesState)
	return f
}

type filesState = filespane.State
type fileEntry = filespane.Entry
type fileDirectory = filespane.Directory
type fileSearch = filespane.Search
type filePreview = filespane.Preview

func newFilesState(root string) *filesState { return filespane.NewState(root) }

func (a *App) startFiles(dir string) *tab {
	if dir == "" {
		dir = a.opts.Cwd
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}
	if dir != "" && !isRemote(dir) {
		dir = filepath.Clean(dir)
	}
	t := &tab{id: newTabID(), dir: dir, pane: newFilesState(dir)}
	a.tabs = append(a.tabs, t)
	return t
}

func (a *App) openFiles(dir string) { a.activate(a.startFiles(dir)) }

// followFiles changes every Files tab to the latest focused non-Files
// tab's working folder. Focusing Files itself never changes that folder.
func (a *App) changeFilesRoot(tb *tab, dir string) {
	if tb == nil || tb.filesState() == nil || isRemote(dir) {
		return
	}
	dir = filepath.Clean(dir)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		a.notice = "Choose an existing folder for the Files pane."
		return
	}
	tb.dir = dir
	tb.filesState().PinnedRoot = true
	a.refreshFiles(tb)
}

func (a *App) followFiles(dir string) {
	if dir == "" {
		return
	}
	for _, tb := range a.tabs {
		if tb.filesState() == nil || tb.filesState().PinnedRoot || tb.dir == dir {
			continue
		}
		old := tb.filesState()
		query, hidden, ignored, wrap, width := old.Query, old.ShowHidden, old.ShowIgnored, old.Wrap, old.TreeW
		old.Close()
		tb.dir, tb.pane = dir, newFilesState(dir)
		tb.filesState().Query, tb.filesState().ShowHidden, tb.filesState().ShowIgnored, tb.filesState().Wrap, tb.filesState().TreeW = query, hidden, ignored, wrap, width
		if tb.filesState().HasQuery() {
			a.searchFiles(tb)
		}
	}
}

// filesWork runs readers off the window thread. Headless tests have no
// redraw callback and run the same work inline for deterministic frames.
func (a *App) filesWork(work func()) {
	if a.redraw == nil {
		work()
	} else {
		go work()
	}
}

func (a *App) filesResult(fn func()) {
	a.update(func() {
		select {
		case <-a.done:
			return
		default:
		}
		fn()
	})
}

func (a *App) loadFilesDir(tb *tab, path string) {
	f := tb.filesState()
	if f == nil {
		return
	}
	remote := isRemote(f.Root)
	var source remoteFiles
	if remote {
		source = a.remoteFiles(f.Root)
	}
	d := f.Dirs[path]
	if d == nil {
		d = &fileDirectory{}
		f.Dirs[path] = d
	}
	if d.Loaded || d.Loading {
		return
	}
	d.Loading = true
	a.filesWork(func() {
		var entries []fileEntry
		var err error
		if remote {
			var reply remoteFileReply
			reply, err = source.call(remoteFileRequest{Op: "list", Path: path, Hidden: f.ShowHidden, Ignored: f.ShowIgnored})
			entries = localEntries(reply.Entries, source.name)
		} else {
			entries, err = readFilesDir(f.Ctx, f.Root, path, f.ShowHidden, f.ShowIgnored)
		}
		a.filesResult(func() {
			if tb.filesState() != f || f.Ctx.Err() != nil {
				return
			}
			d.Entries, d.Loaded, d.Loading = entries, true, false
			if err != nil {
				d.Err = err.Error()
			}
		})
	})
}

func readFilesDir(ctx context.Context, root, path string, showHidden, showIgnored bool) ([]fileEntry, error) {
	return filespane.ReadDir(ctx, root, path, showHidden, showIgnored)
}

// .git is repository machinery, so showing dotfiles alone does not bring
// it into the everyday tree. Show ignored files reveals it explicitly.
func fileNameVisible(name string, showHidden, showIgnored bool) bool {
	return filespane.NameVisible(name, showHidden, showIgnored)
}

// gitIgnored uses Git's own nested ignore rules, batching all children of
// a directory into one process. Outside a repository it excludes nothing.
func gitIgnored(ctx context.Context, root string, paths []string) map[string]bool {
	return filespane.GitIgnored(ctx, root, paths)
}

func (a *App) searchFiles(tb *tab) {
	f := tb.filesState()
	if f == nil {
		return
	}
	if f.SearchCancel != nil {
		f.SearchCancel()
	}
	f.SearchSeq++
	f.Search = fileSearch{}
	query := strings.TrimSpace(f.Query)
	if query == "" {
		return
	}
	remote := isRemote(f.Root)
	var source remoteFiles
	if remote {
		source = a.remoteFiles(f.Root)
	}
	ctx, cancel := context.WithCancel(f.Ctx)
	f.SearchCancel = cancel
	seq := f.SearchSeq
	f.Search.Loading = true
	a.filesWork(func() {
		// A short pause avoids walking the tree for every keystroke.
		if a.redraw != nil {
			timer := time.NewTimer(150 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		var result fileSearch
		if remote {
			reply, err := source.call(remoteFileRequest{Op: "search", Path: f.Root, Query: query, Hidden: f.ShowHidden, Ignored: f.ShowIgnored})
			result = fileSearch{Tree: map[string][]fileEntry{}, Matches: reply.Matches, More: reply.More}
			if err != nil {
				result.Err = err.Error()
			} else {
				for path, entries := range reply.Tree {
					result.Tree[remotePath(source.name, path)] = localEntries(entries, source.name)
				}
			}
		} else {
			result = findFiles(ctx, f.Root, query, f.ShowHidden, f.ShowIgnored)
		}
		a.filesResult(func() {
			if tb.filesState() == f && f.SearchSeq == seq && ctx.Err() == nil {
				f.Search = result
				for _, entries := range result.Tree {
					for _, e := range entries {
						if e.Dir {
							open := true
							f.Open[e.Path] = &open
						}
					}
				}
			}
		})
	})
}

func findFiles(ctx context.Context, root, query string, showHidden, showIgnored bool) fileSearch {
	return filespane.Find(ctx, root, query, showHidden, showIgnored)
}

func (a *App) selectFile(tb *tab, path string, dir bool) {
	f := tb.filesState()
	if f == nil {
		return
	}
	f.Selected = path
	if f.Narrow {
		f.PreviewOnly = !dir && path != ""
	}
	f.PreviewSeq++
	f.ClosePreview()
	f.Preview = filePreview{}
	if dir || path == "" {
		return
	}
	seq := f.PreviewSeq
	f.Preview = filePreview{Path: path, Loading: true}
	remote := isRemote(f.Root)
	var source remoteFiles
	if remote {
		source = a.remoteFiles(f.Root)
	}
	a.filesWork(func() {
		var preview filePreview
		if remote {
			reply, err := source.call(remoteFileRequest{Op: "preview", Path: path})
			preview = filePreview{Path: path, Text: reply.Text, Note: reply.Note, Size: reply.Size}
			if err != nil {
				preview.Note = "Can't read this file: " + err.Error()
			} else if len(reply.Image) > 0 {
				preview.Image, err = ui.DecodeBitmap(reply.Image)
				if err != nil {
					preview.Note = "This image could not be decoded."
				}
			}
		} else {
			preview = readFilePreview(path)
		}
		a.filesResult(func() {
			if tb.filesState() == f && f.PreviewSeq == seq && f.Ctx.Err() == nil {
				if preview.Pdf {
					if runtime.GOOS == "darwin" && a.newFileWebView != nil {
						web, err := a.newFileWebView()
						if err == nil {
							preview.Web = web
							preview.Loading = true
							preview.WebOff = append(preview.WebOff,
								web.Page().OnDidNavigate(func(string) {
									a.filesResult(func() {
										if tb.filesState() == f && f.PreviewSeq == seq {
											f.Preview.Loading = false
										}
									})
								}),
								web.Page().OnDidFinishLoad(func() {
									a.filesResult(func() {
										if tb.filesState() == f && f.PreviewSeq == seq {
											f.Preview.Loading = false
										}
									})
								}),
								web.Page().OnDidFailLoad(func(loadErr *mygo.LoadError) {
									a.filesResult(func() {
										if tb.filesState() == f && f.PreviewSeq == seq {
											f.Preview.Loading = false
											f.Preview.Note = "Can't preview this PDF: " + loadErr.Description
										}
									})
								}),
							)
							err = web.Page().LoadFile(path)
						}
						if err != nil {
							preview.Loading = false
							preview.Note = "Can't preview this PDF: " + err.Error()
						}
					} else {
						preview.Note = "PDF preview is available in local macOS Files tabs. Open it externally to view it."
					}
				} else if preview.Note == "" && preview.Image == nil {
					buf := editor.NewBuffer(preview.Text)
					buf.SetLanguage(editor.LanguageFor(path))
					preview.Code = &editor.State{Buffer: buf, ReadOnly: true, SoftWrap: f.Wrap, NoFolding: true, NoActiveLine: true, NoScrollPastEnd: true, Label: "Preview " + filepath.Base(path)}
				}
				f.Preview = preview
			}
		})
	})
}

func readFilePreview(path string) filePreview { return filespane.ReadPreview(path) }

func isPreviewImage(path string) bool { return filespane.IsPreviewImage(path) }

func (a *App) filesPane(c *ui.Context, theme *ui.Theme, tb *tab) ui.Element {
	f := tb.filesState()
	return filespane.View(c, tb.id, &f.Narrow, &f.PreviewOnly, &f.TreeW,
		func() { a.filesTree(c, theme, tb) }, func() { a.filesPreview(c, theme, tb) })
}

func (a *App) refreshFiles(tb *tab) {
	f := tb.filesState()
	if f == nil {
		return
	}
	selected, query := f.Selected, f.Query
	selectedDir := f.Preview.Path == ""
	f.Close()
	next := newFilesState(tb.dir)
	next.ShowHidden, next.ShowIgnored, next.Wrap = f.ShowHidden, f.ShowIgnored, f.Wrap
	next.TreeW, next.Narrow, next.PreviewOnly, next.FocusSearch = f.TreeW, f.Narrow, f.PreviewOnly, f.FocusSearch
	next.PinnedRoot = f.PinnedRoot
	next.Query = query
	next.Anchor = f.Anchor
	next.Picked = slices.Clone(f.Picked)
	for path, open := range f.Open {
		value := *open
		next.Open[path] = &value
	}
	tb.pane = next
	if next.HasQuery() {
		a.searchFiles(tb)
	}
	if selected != "" && filePathVisible(next, selected) {
		a.selectFile(tb, selected, selectedDir)
	} else {
		next.PreviewOnly = false
	}
	next.Picked = slices.DeleteFunc(next.Picked, func(path string) bool { return !filePathVisible(next, path) })
}

func filePathVisible(f *filesState, path string) bool {
	rel, err := filepath.Rel(f.Root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	if _, err := os.Lstat(path); err != nil && !isRemote(f.Root) {
		return false
	}
	for _, name := range strings.Split(rel, string(filepath.Separator)) {
		if !fileNameVisible(name, f.ShowHidden, f.ShowIgnored) {
			return false
		}
	}
	return isRemote(f.Root) || f.ShowIgnored || !gitIgnored(f.Ctx, f.Root, []string{path})[path]
}

func (a *App) filesTree(c *ui.Context, theme *ui.Theme, tb *tab) {
	f := tb.filesState()
	filespane.TreeView(c, theme, filespane.TreeViewOptions{
		State: f, RootLabel: tildePath(f.Root), RefreshShortcut: keyText("Cmd+R"),
		FolderIcon: iconFolder, ReloadIcon: iconReload, FilterIcon: iconFilter,
		Bulk: a.filesBulkAvailable(tb), CanBrowse: a.pickDir != nil && !isRemote(f.Root),
		Menu: func(m *ui.Menu, path string, dir, root bool) { a.filesMenu(m, tb, path, dir, root) },
		DropTarget: func(item ui.Element, path string, opacity float32) {
			if _, over := ui.DragOver[filesDrag](item); over {
				item.Background(theme.Accent.Alpha(opacity))
			}
			if drag, ok := ui.Drop[filesDrag](item); ok {
				a.moveFilesDrop(drag, tb, path)
			}
		},
		SmallButton: func(key string, icon *ui.SVG, tip string, fn func()) { a.smallButton(c, theme, key, icon, tip, fn) },
		IconButton: func(key string, icon *ui.SVG, tip string, on bool, fn func()) {
			a.iconButton(c, theme, key, icon, tip, on, fn)
		},
		BrowseRoot: func() { a.pickDir(f.Root, func(dir string) { a.changeFilesRoot(tb, dir) }) },
		Refresh:    func() { a.refreshFiles(tb) }, Search: func() { a.searchFiles(tb) },
		LoadDir:    func(path string) { a.loadFilesDir(tb, path) },
		SelectNone: func() { a.selectFile(tb, "", false) },
		Trash:      func(paths []string) { a.askTrashFilesEntries(tb, paths) },
		ClickFile:  func(path string, dir bool, mods ui.Modifiers) { a.clickFile(tb, path, dir, mods) },
		RightClick: func(path string, dir bool) { a.filesRightClick(tb, path, dir) },
		DragItem:   func(item ui.Element, e fileEntry) { a.filesDragItem(c, item, tb, e) },
	})
}

func (a *App) filesPreview(c *ui.Context, theme *ui.Theme, tb *tab) {
	filespane.PreviewView(c, theme, filespane.PreviewViewOptions{
		State: tb.filesState(), Opacity: a.paneOpacity,
		BackIcon: iconBack, WrapIcon: iconWrap, ExternalIcon: iconExternal, FolderIcon: iconFolder,
		IconButton: func(key string, icon *ui.SVG, tip string, on bool, fn func()) {
			a.iconButton(c, theme, key, icon, tip, on, fn)
		},
		OpenPath: a.openPath, ShowFile: a.showFile,
	})
}

func filesRevealLabel() string { return filespane.RevealLabel() }
