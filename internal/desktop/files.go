package desktop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

const (
	maxFilePreview  = 512 << 10
	maxImagePreview = 10 << 20
	maxFileMatches  = 500
)

// filesState belongs to one Files tab. The background readers only return
// immutable results; the window's update callback applies them on its frame.
type filesState struct {
	root, query, selected  string
	picked, rows, lastRows []string
	anchor                 string
	showHidden             bool
	showIgnored            bool
	wrap                   bool
	focusSearch            bool
	treeW                  float32
	narrow, previewOnly    bool
	dirs                   map[string]*fileDirectory
	open                   map[string]*bool
	search                 fileSearch
	preview                filePreview
	ctx                    context.Context
	cancel                 context.CancelFunc
	searchCancel           context.CancelFunc
	searchSeq              uint64
	previewSeq             uint64
}

type fileEntry struct {
	path string
	dir  bool
}

type fileDirectory struct {
	entries []fileEntry
	err     string
	loaded  bool
	loading bool
}

type fileSearch struct {
	tree    map[string][]fileEntry
	matches int
	more    bool
	loading bool
	err     string
}

type filePreview struct {
	path, text, note string
	image            *ui.Bitmap
	code             *editor.State
	pdf              bool
	web              *mygo.WebView
	webOff           []func()
	size             int64
	loading          bool
}

func (f *filesState) hasQuery() bool { return strings.TrimSpace(f.query) != "" }

func newFilesState(root string) *filesState {
	ctx, cancel := context.WithCancel(context.Background())
	return &filesState{root: root, treeW: 220, dirs: map[string]*fileDirectory{}, open: map[string]*bool{}, ctx: ctx, cancel: cancel}
}

func (f *filesState) close() {
	f.closePreview()
	if f.searchCancel != nil {
		f.searchCancel()
	}
	f.cancel()
}

func (f *filesState) closePreview() {
	for _, off := range f.preview.webOff {
		off()
	}
	if f.preview.web != nil {
		f.preview.web.Destroy()
	}
	f.preview.webOff = nil
	f.preview.web = nil
}

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
	t := &tab{id: newTabID(), dir: dir, files: newFilesState(dir)}
	a.tabs = append(a.tabs, t)
	return t
}

func (a *App) openFiles(dir string) { a.activate(a.startFiles(dir)) }

// followFiles changes every Files tab to the latest focused non-Files
// tab's working folder. Focusing Files itself never changes that folder.
func (a *App) followFiles(dir string) {
	if dir == "" {
		return
	}
	for _, tb := range a.tabs {
		if tb.files == nil || tb.dir == dir {
			continue
		}
		old := tb.files
		query, hidden, ignored, wrap, width := old.query, old.showHidden, old.showIgnored, old.wrap, old.treeW
		old.close()
		tb.dir, tb.files = dir, newFilesState(dir)
		tb.files.query, tb.files.showHidden, tb.files.showIgnored, tb.files.wrap, tb.files.treeW = query, hidden, ignored, wrap, width
		if tb.files.hasQuery() {
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
	f := tb.files
	if f == nil {
		return
	}
	remote := isRemote(f.root)
	var source remoteFiles
	if remote {
		source = a.remoteFiles(f.root)
	}
	d := f.dirs[path]
	if d == nil {
		d = &fileDirectory{}
		f.dirs[path] = d
	}
	if d.loaded || d.loading {
		return
	}
	d.loading = true
	a.filesWork(func() {
		var entries []fileEntry
		var err error
		if remote {
			var reply remoteFileReply
			reply, err = source.call(remoteFileRequest{Op: "list", Path: path, Hidden: f.showHidden, Ignored: f.showIgnored})
			entries = localEntries(reply.Entries, source.name)
		} else {
			entries, err = readFilesDir(f.ctx, f.root, path, f.showHidden, f.showIgnored)
		}
		a.filesResult(func() {
			if tb.files != f || f.ctx.Err() != nil {
				return
			}
			d.entries, d.loaded, d.loading = entries, true, false
			if err != nil {
				d.err = err.Error()
			}
		})
	})
}

func readFilesDir(ctx context.Context, root, path string, showHidden, showIgnored bool) ([]fileEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	items, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(items))
	for _, item := range items {
		if !fileNameVisible(item.Name(), showHidden, showIgnored) {
			continue
		}
		paths = append(paths, filepath.Join(path, item.Name()))
	}
	ignored := map[string]bool{}
	if !showIgnored {
		ignored = gitIgnored(ctx, root, paths)
	}
	entries := make([]fileEntry, 0, len(paths))
	for _, item := range items {
		p := filepath.Join(path, item.Name())
		if !fileNameVisible(item.Name(), showHidden, showIgnored) || ignored[p] {
			continue
		}
		entries = append(entries, fileEntry{path: p, dir: item.IsDir()})
	}
	slices.SortFunc(entries, func(x, y fileEntry) int {
		if x.dir != y.dir {
			if x.dir {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(filepath.Base(x.path)), strings.ToLower(filepath.Base(y.path)))
	})
	return entries, ctx.Err()
}

// .git is repository machinery, so showing dotfiles alone does not bring
// it into the everyday tree. Show ignored files reveals it explicitly.
func fileNameVisible(name string, showHidden, showIgnored bool) bool {
	if name == ".git" {
		return showIgnored
	}
	return showHidden || !strings.HasPrefix(name, ".")
}

// gitIgnored uses Git's own nested ignore rules, batching all children of
// a directory into one process. Outside a repository it excludes nothing.
func gitIgnored(ctx context.Context, root string, paths []string) map[string]bool {
	ignored := map[string]bool{}
	if len(paths) == 0 {
		return ignored
	}
	cmd := exec.CommandContext(ctx, "git", "-C", root, "check-ignore", "--stdin", "-z")
	var input bytes.Buffer
	for _, p := range paths {
		input.WriteString(p)
		input.WriteByte(0)
	}
	cmd.Stdin = &input
	out, err := cmd.Output()
	if err != nil {
		return ignored // exit 1 means nothing was ignored; 128 means no Git repository
	}
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) > 0 {
			ignored[string(p)] = true
		}
	}
	return ignored
}

func (a *App) searchFiles(tb *tab) {
	f := tb.files
	if f == nil {
		return
	}
	if f.searchCancel != nil {
		f.searchCancel()
	}
	f.searchSeq++
	f.search = fileSearch{}
	query := strings.TrimSpace(f.query)
	if query == "" {
		return
	}
	remote := isRemote(f.root)
	var source remoteFiles
	if remote {
		source = a.remoteFiles(f.root)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	f.searchCancel = cancel
	seq := f.searchSeq
	f.search.loading = true
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
			reply, err := source.call(remoteFileRequest{Op: "search", Path: f.root, Query: query, Hidden: f.showHidden, Ignored: f.showIgnored})
			result = fileSearch{tree: map[string][]fileEntry{}, matches: reply.Matches, more: reply.More}
			if err != nil {
				result.err = err.Error()
			} else {
				for path, entries := range reply.Tree {
					result.tree[remotePath(source.name, path)] = localEntries(entries, source.name)
				}
			}
		} else {
			result = findFiles(ctx, f.root, query, f.showHidden, f.showIgnored)
		}
		a.filesResult(func() {
			if tb.files == f && f.searchSeq == seq && ctx.Err() == nil {
				f.search = result
				for _, entries := range result.tree {
					for _, e := range entries {
						if e.dir {
							open := true
							f.open[e.path] = &open
						}
					}
				}
			}
		})
	})
}

func findFiles(ctx context.Context, root, query string, showHidden, showIgnored bool) fileSearch {
	r := fileSearch{tree: map[string][]fileEntry{}}
	query = strings.ToLower(query)
	var walk func(string)
	walk = func(dir string) {
		if ctx.Err() != nil || r.more {
			return
		}
		entries, err := readFilesDir(ctx, root, dir, showHidden, showIgnored)
		if err != nil {
			if dir == root && !errors.Is(err, context.Canceled) {
				r.err = err.Error()
			}
			return
		}
		for _, e := range entries {
			if ctx.Err() != nil {
				return
			}
			if strings.Contains(strings.ToLower(filepath.Base(e.path)), query) {
				if r.matches >= maxFileMatches {
					r.more = true
					return
				}
				r.matches++
				for p := e.path; p != root; p = filepath.Dir(p) {
					parent := filepath.Dir(p)
					child := fileEntry{path: p, dir: p != e.path || e.dir}
					found := false
					for _, old := range r.tree[parent] {
						if old.path == p {
							found = true
							break
						}
					}
					if !found {
						r.tree[parent] = append(r.tree[parent], child)
					}
				}
			}
			if e.dir {
				walk(e.path)
			}
		}
	}
	walk(root)
	for _, entries := range r.tree {
		slices.SortFunc(entries, func(x, y fileEntry) int {
			if x.dir != y.dir {
				if x.dir {
					return -1
				}
				return 1
			}
			return strings.Compare(strings.ToLower(filepath.Base(x.path)), strings.ToLower(filepath.Base(y.path)))
		})
	}
	return r
}

func (a *App) selectFile(tb *tab, path string, dir bool) {
	f := tb.files
	if f == nil {
		return
	}
	f.selected = path
	if f.narrow {
		f.previewOnly = !dir && path != ""
	}
	f.previewSeq++
	f.closePreview()
	f.preview = filePreview{}
	if dir || path == "" {
		return
	}
	seq := f.previewSeq
	f.preview = filePreview{path: path, loading: true}
	remote := isRemote(f.root)
	var source remoteFiles
	if remote {
		source = a.remoteFiles(f.root)
	}
	a.filesWork(func() {
		var preview filePreview
		if remote {
			reply, err := source.call(remoteFileRequest{Op: "preview", Path: path})
			preview = filePreview{path: path, text: reply.Text, note: reply.Note, size: reply.Size}
			if err != nil {
				preview.note = "Can't read this file: " + err.Error()
			} else if len(reply.Image) > 0 {
				preview.image, err = ui.DecodeBitmap(reply.Image)
				if err != nil {
					preview.note = "This image could not be decoded."
				}
			}
		} else {
			preview = readFilePreview(path)
		}
		a.filesResult(func() {
			if tb.files == f && f.previewSeq == seq && f.ctx.Err() == nil {
				if preview.pdf {
					if runtime.GOOS == "darwin" && a.newFileWebView != nil {
						web, err := a.newFileWebView()
						if err == nil {
							preview.web = web
							preview.loading = true
							preview.webOff = append(preview.webOff,
								web.Page().OnDidNavigate(func(string) {
									a.filesResult(func() {
										if tb.files == f && f.previewSeq == seq {
											f.preview.loading = false
										}
									})
								}),
								web.Page().OnDidFinishLoad(func() {
									a.filesResult(func() {
										if tb.files == f && f.previewSeq == seq {
											f.preview.loading = false
										}
									})
								}),
								web.Page().OnDidFailLoad(func(loadErr *mygo.LoadError) {
									a.filesResult(func() {
										if tb.files == f && f.previewSeq == seq {
											f.preview.loading = false
											f.preview.note = "Can't preview this PDF: " + loadErr.Description
										}
									})
								}),
							)
							err = web.Page().LoadFile(path)
						}
						if err != nil {
							preview.loading = false
							preview.note = "Can't preview this PDF: " + err.Error()
						}
					} else {
						preview.note = "PDF preview is available in local macOS Files tabs. Open it externally to view it."
					}
				} else if preview.note == "" && preview.image == nil {
					buf := editor.NewBuffer(preview.text)
					buf.SetLanguage(editor.LanguageFor(path))
					preview.code = &editor.State{Buffer: buf, ReadOnly: true, SoftWrap: f.wrap, NoFolding: true, NoActiveLine: true, NoScrollPastEnd: true, Label: "Preview " + filepath.Base(path)}
				}
				f.preview = preview
			}
		})
	})
}

func readFilePreview(path string) filePreview {
	r := filePreview{path: path}
	info, err := os.Stat(path)
	if err != nil {
		r.note = fmt.Sprintf("Can't read this file: %v", err)
		return r
	}
	r.size = info.Size()
	if !info.Mode().IsRegular() {
		r.note = "This item cannot be previewed."
		return r
	}
	if runtime.GOOS == "darwin" && strings.EqualFold(filepath.Ext(path), ".pdf") {
		file, err := os.Open(path)
		if err != nil {
			r.note = fmt.Sprintf("Can't read this file: %v", err)
			return r
		}
		defer file.Close()
		var header [5]byte
		if _, err := file.Read(header[:]); err != nil || string(header[:]) != "%PDF-" {
			r.note = "This PDF could not be opened. Open it externally to view it."
			return r
		}
		r.pdf = true
		return r
	}
	image := isPreviewImage(path)
	limit := int64(maxFilePreview)
	if image {
		limit = maxImagePreview
	}
	if info.Size() > limit {
		r.note = fmt.Sprintf("This file is too large to preview (%.1f MB). Open it externally to view it.", float64(info.Size())/(1<<20))
		return r
	}
	data, err := os.ReadFile(path)
	if err != nil {
		r.note = fmt.Sprintf("Can't read this file: %v", err)
		return r
	}
	if image {
		r.image, err = ui.DecodeBitmap(data)
		if err != nil {
			r.note = "This image could not be decoded. Open it externally to view it."
		}
		return r
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		r.note = "This binary file cannot be previewed. Open it externally to view it."
		return r
	}
	r.text = string(data)
	if r.text == "" {
		r.note = "This file is empty."
	}
	return r
}

func isPreviewImage(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg":
		return true
	}
	return false
}

func (a *App) filesPane(c *ui.Context, theme *ui.Theme, tb *tab) ui.Element {
	f := tb.files
	pane := ui.Column(c.Key(tb.id + "-files")).Grow(1).MinWidth(0).Focusable()
	if width := pane.Bounds().W; width > 0 {
		f.narrow = width < 520
		if !f.narrow {
			f.previewOnly = false
		}
	}
	return pane.Children(func() {
		if f.narrow {
			if f.previewOnly {
				a.filesPreview(c, theme, tb)
			} else {
				a.filesTree(c, theme, tb)
			}
			return
		}
		ui.Split(c, &f.treeW, func() { a.filesTree(c, theme, tb) }, func() { a.filesPreview(c, theme, tb) }).Grow(1)
	})
}

func (a *App) refreshFiles(tb *tab) {
	f := tb.files
	if f == nil {
		return
	}
	selected, query := f.selected, f.query
	selectedDir := f.preview.path == ""
	f.close()
	next := newFilesState(tb.dir)
	next.showHidden, next.showIgnored, next.wrap = f.showHidden, f.showIgnored, f.wrap
	next.treeW, next.narrow, next.previewOnly, next.focusSearch = f.treeW, f.narrow, f.previewOnly, f.focusSearch
	next.query = query
	next.anchor = f.anchor
	next.picked = slices.Clone(f.picked)
	for path, open := range f.open {
		value := *open
		next.open[path] = &value
	}
	tb.files = next
	if next.hasQuery() {
		a.searchFiles(tb)
	}
	if selected != "" && filePathVisible(next, selected) {
		a.selectFile(tb, selected, selectedDir)
	} else {
		next.previewOnly = false
	}
	next.picked = slices.DeleteFunc(next.picked, func(path string) bool { return !filePathVisible(next, path) })
}

func filePathVisible(f *filesState, path string) bool {
	rel, err := filepath.Rel(f.root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	if _, err := os.Lstat(path); err != nil && !isRemote(f.root) {
		return false
	}
	for _, name := range strings.Split(rel, string(filepath.Separator)) {
		if !fileNameVisible(name, f.showHidden, f.showIgnored) {
			return false
		}
	}
	return isRemote(f.root) || f.showIgnored || !gitIgnored(f.ctx, f.root, []string{path})[path]
}

func (a *App) filesTree(c *ui.Context, theme *ui.Theme, tb *tab) {
	f := tb.files
	ui.Column(c).Fill().MinWidth(0).Children(func() {
		header := ui.Row(c).Height(40).Gap(6).Padding(0, 8, 0, 12).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
			ui.Icon(c, iconFolder).FontSize(14).TextColor(theme.Accent)
			name := filepath.Base(f.root)
			if name == "." || name == string(filepath.Separator) {
				name = f.root
			}
			ui.Text(c, name).FontSize(12).Bold().SingleLine().Grow(1).Shrink(1).Tooltip(f.root).Label("Explorer folder " + name).
				ContextMenu(func(m *ui.Menu) { a.filesMenu(m, tb, f.root, true, true) })
			a.iconButton(c, theme, "refresh-files", iconReload, "Refresh files", false, func() { a.refreshFiles(tb) })
			ui.Box(c.Key("file-visibility")).Padding(6).Radius(6).Label("File visibility").Tooltip("File visibility").Children(func() {
				ui.Icon(c, iconFilter).FontSize(15).TextColor(theme.Text.Alpha(0.75))
			}).Menu(func(m *ui.Menu) {
				if m.Item("Show hidden files").Checked(f.showHidden).Chosen() {
					f.showHidden = !f.showHidden
					a.refreshFiles(tb)
				}
				if m.Item("Show ignored files").Checked(f.showIgnored).Chosen() {
					f.showIgnored = !f.showIgnored
					a.refreshFiles(tb)
				}
			})
		})
		if a.filesBulkAvailable(tb) {
			if _, over := ui.DragOver[filesDrag](header); over {
				header.Background(theme.Accent.Alpha(0.2))
			}
			if drag, ok := ui.Drop[filesDrag](header); ok {
				a.moveFilesDrop(drag, tb, f.root)
			}
		}
		ui.Row(c).Padding(7, 9).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
			field := ui.SearchField(c.Key("file-search"), &f.query).Placeholder("Find files").Label("Search files and folders").Grow(1).MinWidth(0).
				OnChange(func() { a.searchFiles(tb) }).
				OnShortcut(0, ui.KeyEscape, func() { f.query = ""; a.searchFiles(tb) })
			if f.focusSearch {
				field.Focus()
				f.focusSearch = false
			}
		})
		if f.hasQuery() {
			status := fmt.Sprintf("%d matches", f.search.matches)
			switch {
			case f.search.loading:
				status = "Searching…"
			case f.search.err != "":
				status = f.search.err
			case f.search.more:
				status = fmt.Sprintf("First %d matches · refine your search", maxFileMatches)
			case f.search.matches == 0:
				status = "No matching files or folders"
			}
			ui.Text(c, status).FontSize(11).TextColor(theme.TextMuted).Padding(7, 12)
		}
		ui.Scroll(c.Key("files-scroll")).Grow(1).Children(func() {
			if !f.hasQuery() {
				a.loadFilesDir(tb, f.root)
			}
			f.rows = f.rows[:0]
			tree := ui.Tree(c.Key("files-tree"), func() { a.filesTreeChildren(c, tb, f.root, 0) }).Padding(6, 8)
			if a.filesBulkAvailable(tb) {
				tree.OnShortcut(ui.Super, ui.KeyA, func() {
					f.picked = slices.Clone(f.rows)
					if len(f.picked) > 0 {
						f.anchor = f.picked[0]
					}
				}).
					OnShortcut(0, ui.KeyEscape, func() { f.picked = nil; a.selectFile(tb, "", false) }).
					OnShortcut(ui.Super, ui.KeyBackspace, func() { a.askTrashFilesEntries(tb, slices.Clone(f.picked)) })
			}
			f.lastRows = slices.Clone(f.rows)
		})
	})
}

func (a *App) filesTreeChildren(c *ui.Context, tb *tab, dir string, depth int) {
	if depth > 80 { // avoid building an unbounded widget tree
		return
	}
	f := tb.files
	var entries []fileEntry
	if f.hasQuery() {
		entries = f.search.tree[dir]
	} else {
		d := f.dirs[dir]
		if d == nil || !d.loaded {
			ui.TreeItem(c, "Loading…", nil, nil).Disabled(true)
			return
		}
		if d.err != "" {
			ui.TreeItem(c, "Can't open folder: "+d.err, nil, nil).Disabled(true)
			return
		}
		entries = d.entries
		if len(entries) == 0 && dir == f.root {
			ui.TreeItem(c, "This folder is empty", nil, nil).Disabled(true)
		}
	}
	for _, entry := range entries {
		e := entry
		f.rows = append(f.rows, e.path)
		if e.dir {
			open := f.open[e.path]
			if open == nil {
				initial := f.hasQuery()
				open = &initial
				f.open[e.path] = open
			}
			item := ui.TreeItem(c.Key(e.path), filepath.Base(e.path), open, func() {
				if *open && !f.hasQuery() {
					a.loadFilesDir(tb, e.path)
				}
				a.filesTreeChildren(c, tb, e.path, depth+1)
			}).Selected(a.filePicked(f, e.path))
			if item.Clicked() {
				toggle, extend := multiMods(item.ClickModifiers())
				if !a.filesBulkAvailable(tb) || !toggle && !extend {
					*open = true
					if !f.hasQuery() {
						a.loadFilesDir(tb, e.path)
					}
				}
				a.clickFile(tb, e.path, true, item.ClickModifiers())
			}
			if item.RightClicked() {
				a.filesRightClick(tb, e.path, true)
			}
			item.ContextMenu(func(m *ui.Menu) { a.filesMenu(m, tb, e.path, true, false) })
			a.filesDragItem(item, tb, e)
			if a.filesBulkAvailable(tb) {
				if _, over := ui.DragOver[filesDrag](item); over {
					item.Background(c.Theme().Accent.Alpha(0.2))
				}
				if drag, ok := ui.Drop[filesDrag](item); ok {
					a.moveFilesDrop(drag, tb, e.path)
				}
			}
		} else {
			item := ui.TreeItem(c.Key(e.path), filepath.Base(e.path), nil, nil).Selected(a.filePicked(f, e.path))
			if item.Clicked() {
				a.clickFile(tb, e.path, false, item.ClickModifiers())
			}
			if item.RightClicked() {
				a.filesRightClick(tb, e.path, false)
			}
			item.ContextMenu(func(m *ui.Menu) { a.filesMenu(m, tb, e.path, false, false) })
			a.filesDragItem(item, tb, e)
		}
	}
}

func (a *App) filesPreview(c *ui.Context, theme *ui.Theme, tb *tab) {
	f := tb.files
	ui.Column(c).Fill().MinWidth(0).Children(func() {
		ui.Row(c).Height(48).Gap(7).Padding(0, 9, 0, 12).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
			if f.narrow {
				a.iconButton(c, theme, "back-to-files", iconBack, "Back to Files", false, func() { f.previewOnly = false })
			}
			name := "Preview"
			if f.selected != "" {
				name = filepath.Base(f.selected)
			}
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(1).Children(func() {
				ui.Text(c, name).FontSize(13).Bold().SingleLine().Shrink(1).MinWidth(0).Tooltip(f.selected)
				if f.selected != "" {
					parent, _ := filepath.Rel(f.root, filepath.Dir(f.selected))
					if parent == "." {
						parent = "Project root"
					}
					meta := parent
					if f.preview.path != "" && !f.preview.loading {
						meta += " · " + fileSizeLabel(f.preview.size)
					}
					ui.Text(c, meta).FontSize(10).TextColor(theme.TextMuted).SingleLine().Shrink(1).MinWidth(0).Tooltip(meta)
				}
			})
			if f.preview.path != "" {
				if f.preview.code != nil {
					a.iconButton(c, theme, "wrap-lines", iconWrap, "Wrap lines", f.wrap, func() {
						f.wrap = !f.wrap
						f.preview.code.SoftWrap = f.wrap
					})
				}
				a.iconButton(c, theme, "open-external", iconExternal, "Open externally", false, func() {
					if a.openPath != nil {
						a.openPath(f.selected)
					}
				})
				a.iconButton(c, theme, "reveal-file", iconFolder, filesRevealLabel(), false, func() {
					if a.showFile != nil {
						a.showFile(f.selected)
					}
				})
			}
		})
		if f.selected == "" {
			ui.Column(c).Grow(1).Center().Gap(7).Children(func() {
				ui.Icon(c, iconFolder).FontSize(28).TextColor(theme.TextMuted)
				ui.Text(c, "Select a file to preview").FontSize(13).TextColor(theme.TextMuted)
			})
			return
		}
		p := f.preview
		switch {
		case p.path == "":
			ui.Text(c, "Choose a file in this folder to preview it.").FontSize(12).TextColor(theme.TextMuted).Margin(ui.Auto)
		case p.note != "":
			ui.Text(c, p.note).FontSize(12).TextColor(theme.TextMuted).Padding(14)
		case p.image != nil:
			ui.Image(c, p.image).Grow(1).MinHeight(0).Fit(ui.Contain).Padding(12)
		case p.web != nil:
			ui.WebView(c.Key("pdf-preview"), p.web).Grow(1).MinWidth(0).MinHeight(0).Children(func() {
				if p.loading {
					ui.Text(c, "Loading preview…").FontSize(12).Padding(6, 10).Radius(6).Background(theme.Background).Absolute().Top(12).Left(12)
				}
			})
		case p.loading:
			ui.Text(c, "Loading preview…").FontSize(12).TextColor(theme.TextMuted).Margin(ui.Auto)
		case p.code != nil:
			codeTheme := editor.DefaultTheme(theme)
			if a.paneOpacity < 1 {
				codeTheme.Background = theme.Background.Alpha(0)
			}
			p.code.Theme = codeTheme
			editor.View(c.Key("code-preview"), p.code).Grow(1).MinWidth(0).MinHeight(0)
		default:
			ui.Text(c, "This file cannot be previewed.").FontSize(12).TextColor(theme.TextMuted).Margin(ui.Auto)
		}
	})
}

func fileSizeLabel(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	if size < 1<<20 {
		return fmt.Sprintf("%.1f KB", float64(size)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(size)/(1<<20))
}

func filesRevealLabel() string {
	if runtime.GOOS == "darwin" {
		return "Show in Finder"
	}
	return "Show in file manager"
}
