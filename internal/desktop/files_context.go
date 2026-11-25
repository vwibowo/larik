package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"

	filespane "larik/internal/desktop/panes/files"
)

func (a *App) filesMenu(m *ui.Menu, tb *tab, path string, dir, root bool) {
	f := tb.filesState()
	if f == nil {
		return
	}
	filespane.Menu(m, filespane.MenuOptions{
		State: f, Path: path, Dir: dir, Root: root,
		Bulk: a.filesBulkAvailable(tb), Remote: isRemote(f.Root),
		OpenPath: a.openPath, ShowFile: a.showFile, CopyText: a.copyText,
		BeginEdit: func(path, kind string) { a.beginFilesEdit(tb, path, kind) },
		Duplicate: func(path string) { a.duplicateFilesEntry(tb, path) },
		Move:      func(paths []string) { a.beginMoveFiles(tb, paths) },
		Trash: func(paths []string, bulk bool) {
			if bulk {
				a.askTrashFilesEntries(tb, paths)
			} else {
				a.askTrashFilesEntry(tb, path)
			}
		},
	})
}

func filesWithinRoot(root, path string) bool { return filespane.WithinRoot(root, path) }

type filesEdit struct {
	tb               *tab
	path, kind, name string
	err              string
	open, focus      bool
	busy             bool
}

func (a *App) beginFilesEdit(tb *tab, path, kind string) {
	name := ""
	if kind == "rename" {
		name = filepath.Base(path)
	}
	a.filesEdit = &filesEdit{tb: tb, path: path, kind: kind, name: name, open: true, focus: true}
}

func (a *App) filesEditDialog(c *ui.Context) {
	e := a.filesEdit
	if e == nil {
		return
	}
	if e.tb.filesState() == nil || !filesWithinRoot(e.tb.filesState().Root, e.path) {
		a.filesEdit = nil
		return
	}
	filespane.EditDialog(c, filespane.EditDialogOptions{
		Open: &e.open, Path: e.path, Kind: e.kind, Name: &e.name,
		Error: e.err, Busy: e.busy, Focus: &e.focus,
		Commit: func() { a.commitFilesEdit(e) },
		Cancel: func() { a.filesEdit = nil },
	})
	if !e.open && a.filesEdit == e {
		a.filesEdit = nil
	}
}

func (a *App) commitFilesEdit(e *filesEdit) {
	if e.busy {
		return
	}
	if e.tb.filesState() == nil || !filesWithinRoot(e.tb.filesState().Root, e.path) {
		a.filesEdit = nil
		return
	}
	name := strings.TrimSpace(e.name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) || strings.IndexByte(name, 0) >= 0 {
		e.err = "Enter a file or folder name without a path separator."
		return
	}
	parent := e.path
	if e.kind == "rename" {
		if e.path == e.tb.filesState().Root {
			e.err = "The explorer folder cannot be renamed here."
			return
		}
		parent = filepath.Dir(e.path)
	}
	dest := filepath.Join(parent, name)
	if !filesWithinRoot(e.tb.filesState().Root, dest) {
		e.err = "The name must stay inside this folder."
		return
	}
	if e.kind == "rename" && dest == e.path {
		a.filesEdit = nil
		return
	}
	if isRemote(e.tb.filesState().Root) {
		e.busy = true
		source := a.remoteFiles(e.tb.filesState().Root)
		op := map[string]string{"file": "create-file", "folder": "create-folder", "rename": "rename"}[e.kind]
		root := e.tb.filesState().Root
		a.filesWork(func() {
			reply, err := source.call(remoteFileRequest{Op: op, Path: e.path, Name: name})
			a.filesResult(func() {
				e.busy = false
				if a.filesEdit != e || e.tb.filesState() == nil || e.tb.filesState().Root != root {
					return
				}
				if err != nil {
					e.err = err.Error()
					return
				}
				a.filesEdit = nil
				a.refreshFiles(e.tb)
				open := true
				e.tb.filesState().Open[parent] = &open
				if e.kind == "folder" {
					e.tb.filesState().Open[remotePath(source.name, reply.Dest)] = &open
				}
				a.selectFile(e.tb, remotePath(source.name, reply.Dest), reply.Dir)
			})
		})
		return
	}
	if _, err := os.Lstat(dest); err == nil {
		e.err = "An item with that name already exists."
		return
	} else if !os.IsNotExist(err) {
		e.err = err.Error()
		return
	}
	var err error
	switch e.kind {
	case "file":
		var file *os.File
		file, err = os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			err = file.Close()
		}
	case "folder":
		err = os.Mkdir(dest, 0o755)
	case "rename":
		err = os.Rename(e.path, dest)
	default:
		err = fmt.Errorf("unknown file action")
	}
	if err != nil {
		e.err = err.Error()
		return
	}
	a.filesEdit = nil
	if e.kind == "rename" {
		for old, expanded := range e.tb.filesState().Open {
			if old == e.path || strings.HasPrefix(old, e.path+string(filepath.Separator)) {
				e.tb.filesState().Open[dest+strings.TrimPrefix(old, e.path)] = expanded
				delete(e.tb.filesState().Open, old)
			}
		}
	}
	a.refreshFiles(e.tb)
	open := true
	e.tb.filesState().Open[parent] = &open
	if e.kind == "folder" {
		e.tb.filesState().Open[dest] = &open
	}
	if filePathVisible(e.tb.filesState(), dest) {
		a.selectFile(e.tb, dest, e.kind == "folder" || (e.kind == "rename" && isDirectory(dest)))
	}
}

func isDirectory(path string) bool { return filespane.IsDirectory(path) }

func (a *App) duplicateFilesEntry(tb *tab, path string) {
	if tb.filesState() == nil || path == tb.filesState().Root || !filesWithinRoot(tb.filesState().Root, path) {
		return
	}
	root := tb.filesState().Root
	remote := isRemote(root)
	var source remoteFiles
	if remote {
		source = a.remoteFiles(root)
	}
	a.filesWork(func() {
		var to string
		var dir bool
		var err error
		if remote {
			var reply remoteFileReply
			reply, err = source.call(remoteFileRequest{Op: "duplicate", Path: path})
			to, dir = remotePath(source.name, reply.Dest), reply.Dir
		} else {
			to, err = duplicateFilesPath(path)
			dir = isDirectory(to)
		}
		a.filesResult(func() {
			if err != nil {
				a.notice = "Can't duplicate “" + filepath.Base(path) + "”: " + err.Error()
				return
			}
			if tb.filesState() != nil && tb.filesState().Root == root {
				a.refreshFiles(tb)
				if filePathVisible(tb.filesState(), to) {
					a.selectFile(tb, to, dir)
				}
			}
		})
	})
}

func duplicateFilesPath(path string) (string, error) { return filespane.DuplicatePath(path) }

func copyFilesPath(from, to string, info os.FileInfo) error {
	return filespane.CopyPath(from, to, info)
}

func (a *App) askTrashFilesEntry(tb *tab, path string) {
	if tb.filesState() == nil || path == tb.filesState().Root || !filesWithinRoot(tb.filesState().Root, path) {
		return
	}
	remote := isRemote(tb.filesState().Root)
	if a.trashItem == nil && !remote {
		a.notice = "The system Trash is unavailable."
		return
	}
	name := filepath.Base(path)
	root := tb.filesState().Root
	var source remoteFiles
	message := "You can restore it from the system Trash until the Trash is emptied."
	if remote {
		source = a.remoteFiles(root)
		message = "It moves to the remote Larik trash folder, where you can restore it."
	}
	a.confirmAsk(fmt.Sprintf("Move “%s” to the Trash?", name),
		message,
		"Move to Trash", func() {
			a.filesWork(func() {
				var err error
				if remote {
					_, err = source.call(remoteFileRequest{Op: "trash", Path: path})
				} else {
					err = a.trashItem(path)
				}
				a.filesResult(func() {
					if err != nil {
						a.notice = "Can't move “" + name + "” to the Trash: " + err.Error()
						return
					}
					if tb.filesState() != nil && tb.filesState().Root == root {
						a.refreshFiles(tb)
					}
				})
			})
		})
}
