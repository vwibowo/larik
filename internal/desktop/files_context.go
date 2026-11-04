package desktop

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// Files operations act on the path whose menu was opened, rather than the
// preview's current selection. The root itself can be opened and populated,
// but cannot be renamed or moved from inside its own explorer.
func (a *App) filesMenu(m *ui.Menu, tb *tab, path string, dir, root bool) {
	if tb.files == nil || isRemote(tb.files.root) || !filesWithinRoot(tb.files.root, path) {
		return
	}
	if dir {
		if m.Item("New File…").Chosen() {
			a.beginFilesEdit(tb, path, "file")
		}
		if m.Item("New Folder…").Chosen() {
			a.beginFilesEdit(tb, path, "folder")
		}
		m.Separator()
	}
	if m.Item("Open externally").Chosen() && a.openPath != nil {
		a.openPath(path)
	}
	if m.Item(filesRevealLabel()).Chosen() && a.showFile != nil {
		a.showFile(path)
	}
	if m.Item("Copy Path").Chosen() && a.copyText != nil {
		a.copyText(path)
	}
	if !root {
		if m.Item("Copy Relative Path").Chosen() && a.copyText != nil {
			rel, _ := filepath.Rel(tb.files.root, path)
			a.copyText(rel)
		}
		m.Separator()
		if m.Item("Rename…").Chosen() {
			a.beginFilesEdit(tb, path, "rename")
		}
		if m.Item("Duplicate").Chosen() {
			a.duplicateFilesEntry(tb, path)
		}
		if m.Item("Move to Trash…").Chosen() {
			a.askTrashFilesEntry(tb, path)
		}
	}
}

func filesWithinRoot(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

type filesEdit struct {
	tb               *tab
	path, kind, name string
	err              string
	open, focus      bool
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
	if e.tb.files == nil || isRemote(e.tb.files.root) || !filesWithinRoot(e.tb.files.root, e.path) {
		a.filesEdit = nil
		return
	}
	title := "Rename"
	switch e.kind {
	case "file":
		title = "New File"
	case "folder":
		title = "New Folder"
	}
	ui.Modal(c.Key("files-edit"), &e.open, func() {
		ui.Column(c).Width(340).Gap(10).Children(func() {
			ui.Text(c, title).FontSize(16).Bold()
			ui.Text(c, filepath.Base(e.path)).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine()
			field := ui.TextInput(c.Key("files-name"), &e.name).Label("Name").Placeholder("Name").FillWidth().
				OnSubmit(func() { a.commitFilesEdit(e) })
			if e.focus {
				field.Focus().SetTextSelection(0, utf8.RuneCountInString(e.name))
				e.focus = false
			}
			if e.err != "" {
				ui.Text(c, e.err).FontSize(12).TextColor(c.Theme().Danger)
			}
			ui.Row(c).Gap(8).Justify(ui.End).AlignItems(ui.Center).Children(func() {
				if ui.Button(c.Key("cancel"), "Cancel").Clicked() {
					a.filesEdit = nil
				}
				if ui.Button(c.Key("commit"), title).Clicked() {
					a.commitFilesEdit(e)
				}
			})
		})
	}).Label(title)
	if !e.open && a.filesEdit == e {
		a.filesEdit = nil
	}
}

func (a *App) commitFilesEdit(e *filesEdit) {
	if e.tb.files == nil || isRemote(e.tb.files.root) || !filesWithinRoot(e.tb.files.root, e.path) {
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
		if e.path == e.tb.files.root {
			e.err = "The explorer folder cannot be renamed here."
			return
		}
		parent = filepath.Dir(e.path)
	}
	dest := filepath.Join(parent, name)
	if !filesWithinRoot(e.tb.files.root, dest) {
		e.err = "The name must stay inside this folder."
		return
	}
	if e.kind == "rename" && dest == e.path {
		a.filesEdit = nil
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
		for old, expanded := range e.tb.files.open {
			if old == e.path || strings.HasPrefix(old, e.path+string(filepath.Separator)) {
				e.tb.files.open[dest+strings.TrimPrefix(old, e.path)] = expanded
				delete(e.tb.files.open, old)
			}
		}
	}
	a.refreshFiles(e.tb)
	open := true
	e.tb.files.open[parent] = &open
	if e.kind == "folder" {
		e.tb.files.open[dest] = &open
	}
	if filePathVisible(e.tb.files, dest) {
		a.selectFile(e.tb, dest, e.kind == "folder" || (e.kind == "rename" && isDirectory(dest)))
	}
}

func isDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

func (a *App) duplicateFilesEntry(tb *tab, path string) {
	if tb.files == nil || path == tb.files.root || !filesWithinRoot(tb.files.root, path) {
		return
	}
	root := tb.files.root
	a.filesWork(func() {
		to, err := duplicateFilesPath(path)
		a.filesResult(func() {
			if err != nil {
				a.notice = "Can't duplicate “" + filepath.Base(path) + "”: " + err.Error()
				return
			}
			if tb.files != nil && tb.files.root == root {
				a.refreshFiles(tb)
				if filePathVisible(tb.files, to) {
					a.selectFile(tb, to, isDirectory(to))
				}
			}
		})
	})
}

func duplicateFilesPath(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	name, ext := filepath.Base(path), ""
	if !info.IsDir() {
		ext = filepath.Ext(name)
		name = strings.TrimSuffix(name, ext)
	}
	for n := 1; n < 10000; n++ {
		suffix := " copy"
		if n > 1 {
			suffix = fmt.Sprintf(" copy %d", n)
		}
		to := filepath.Join(filepath.Dir(path), name+suffix+ext)
		if _, err := os.Lstat(to); !os.IsNotExist(err) {
			if err != nil {
				return "", err
			}
			continue
		}
		if err := copyFilesPath(path, to, info); err != nil {
			if !os.IsExist(err) {
				_ = os.RemoveAll(to)
			}
			return "", err
		}
		return to, nil
	}
	return "", fmt.Errorf("too many copies already exist")
}

func copyFilesPath(from, to string, info os.FileInfo) error {
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(from)
		if err != nil {
			return err
		}
		return os.Symlink(target, to)
	case info.IsDir():
		if err := os.Mkdir(to, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(from)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			src := filepath.Join(from, entry.Name())
			childInfo, err := os.Lstat(src)
			if err != nil {
				return err
			}
			if err := copyFilesPath(src, filepath.Join(to, entry.Name()), childInfo); err != nil {
				return err
			}
		}
		return nil
	case info.Mode().IsRegular():
		in, err := os.Open(from)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
		return err
	default:
		return fmt.Errorf("this item type cannot be duplicated")
	}
}

func (a *App) askTrashFilesEntry(tb *tab, path string) {
	if tb.files == nil || path == tb.files.root || !filesWithinRoot(tb.files.root, path) {
		return
	}
	if a.trashItem == nil {
		a.notice = "The system Trash is unavailable."
		return
	}
	name := filepath.Base(path)
	root := tb.files.root
	a.confirmAsk(fmt.Sprintf("Move “%s” to the Trash?", name),
		"You can restore it from the system Trash until the Trash is emptied.",
		"Move to Trash", func() {
			a.filesWork(func() {
				err := a.trashItem(path)
				a.filesResult(func() {
					if err != nil {
						a.notice = "Can't move “" + name + "” to the Trash: " + err.Error()
						return
					}
					if tb.files != nil && tb.files.root == root {
						a.refreshFiles(tb)
					}
				})
			})
		})
}
