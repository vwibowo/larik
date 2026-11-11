package desktop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"

	"github.com/egoist/mygo/ui"
)

type filesDrag struct {
	source  *tab
	paths   []string
	copying bool
}

func (a *App) filesBulkAvailable(tb *tab) bool {
	return runtime.GOOS == "darwin" && tb != nil && tb.files != nil && !isRemote(tb.files.root)
}

func (a *App) filePicked(f *filesState, path string) bool {
	return slices.Contains(f.picked, path) || len(f.picked) == 0 && f.selected == path
}

func (a *App) clickFile(tb *tab, path string, dir bool, mods ui.Modifiers) {
	f := tb.files
	if !a.filesBulkAvailable(tb) {
		a.selectFile(tb, path, dir)
		return
	}
	toggle, extend := multiMods(mods)
	switch {
	case extend:
		rows := f.lastRows
		if len(rows) == 0 {
			rows = f.rows
		}
		from, to := slices.Index(rows, f.anchor), slices.Index(rows, path)
		if from < 0 {
			from = to
		}
		if from > to {
			from, to = to, from
		}
		if from >= 0 && to >= 0 {
			f.picked = slices.Clone(rows[from : to+1])
		}
	case toggle:
		if i := slices.Index(f.picked, path); i >= 0 {
			f.picked = slices.Delete(f.picked, i, i+1)
		} else {
			f.picked = append(f.picked, path)
		}
		f.anchor = path
	default:
		f.picked, f.anchor = []string{path}, path
	}
	if slices.Contains(f.picked, path) {
		a.selectFile(tb, path, dir)
	} else if len(f.picked) == 0 {
		a.selectFile(tb, "", false)
	} else if f.selected == path {
		remaining := f.picked[len(f.picked)-1]
		a.selectFile(tb, remaining, isDirectory(remaining))
	}
}

func (a *App) filesRightClick(tb *tab, path string, dir bool) {
	if !a.filesBulkAvailable(tb) || slices.Contains(tb.files.picked, path) {
		return
	}
	tb.files.picked, tb.files.anchor = []string{path}, path
	a.selectFile(tb, path, dir)
}

func (a *App) filesDragItem(c *ui.Context, item ui.Element, tb *tab, entry fileEntry) {
	if !a.filesBulkAvailable(tb) {
		return
	}
	paths := []string{entry.path}
	if slices.Contains(tb.files.picked, entry.path) {
		paths = slices.Clone(tb.files.picked)
	}
	item.Drag(filesDrag{source: tb, paths: paths, copying: c.Modifiers()&ui.Alt != 0})
}

func (a *App) moveFilesDrop(d filesDrag, target *tab, dir string) {
	if !a.filesBulkAvailable(d.source) || !a.filesBulkAvailable(target) {
		return
	}
	a.startFilesTransfer(d.source, target, d.paths, dir, d.copying)
}

// normalizeFilesSelection lets an ancestor stand for its selected descendants.
func normalizeFilesSelection(paths []string) []string {
	unique := make([]string, 0, len(paths))
	for _, path := range paths {
		if path != "" && !slices.Contains(unique, path) {
			unique = append(unique, path)
		}
	}
	out := make([]string, 0, len(unique))
	for _, path := range unique {
		nested := false
		for _, other := range unique {
			if other != path && filesWithinRoot(other, path) {
				nested = true
				break
			}
		}
		if !nested {
			out = append(out, path)
		}
	}
	return out
}

type filesMovePicker struct {
	tb    *tab
	paths []string
	dest  string
	open  bool
}

func (a *App) beginMoveFiles(tb *tab, paths []string) {
	if !a.filesBulkAvailable(tb) || len(paths) == 0 || a.filesMoving != nil {
		return
	}
	a.filesMove = &filesMovePicker{tb: tb, paths: normalizeFilesSelection(paths), dest: tb.files.root, open: true}
}

func (a *App) filesMoveDialog(c *ui.Context) {
	p := a.filesMove
	if p == nil {
		return
	}
	if !a.filesBulkAvailable(p.tb) {
		a.filesMove = nil
		return
	}
	ui.Modal(c.Key("files-move"), &p.open, func() {
		ui.Column(c).Width(410).Gap(10).Children(func() {
			ui.Text(c, fmt.Sprintf("Move %d item(s) to…", len(p.paths))).FontSize(16).Bold()
			ui.Text(c, "Choose a folder in this explorer").FontSize(12).TextColor(c.Theme().TextMuted)
			ui.Scroll(c).Height(300).Children(func() {
				ui.Tree(c, func() { a.filesMoveFolders(c, p, p.tb.files.root, 0) })
			})
			ui.Text(c, p.dest).FontSize(11).SingleLine().Tooltip(p.dest)
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					a.filesMove = nil
				}
				if ui.Button(c, "Move").Clicked() {
					a.filesMove = nil
					a.startFilesMove(p.tb, p.tb, p.paths, p.dest)
				}
			})
		})
	}).Label("Move files")
	if !p.open && a.filesMove == p {
		a.filesMove = nil
	}
}

func (a *App) filesMoveFolders(c *ui.Context, p *filesMovePicker, dir string, depth int) {
	if depth > 80 {
		return
	}
	f := p.tb.files
	a.loadFilesDir(p.tb, dir)
	open := f.open[dir]
	if open == nil {
		value := dir == f.root
		open = &value
		f.open[dir] = open
	}
	item := ui.TreeItem(c.Key("move-"+dir), filepath.Base(dir), open, func() {
		if *open {
			if entries := f.dirs[dir]; entries != nil && entries.loaded {
				for _, e := range entries.entries {
					if e.dir {
						a.filesMoveFolders(c, p, e.path, depth+1)
					}
				}
			}
		}
	}).Selected(p.dest == dir)
	if item.Clicked() {
		p.dest = dir
		*open = true
	}
}

type filesMoveBatch struct {
	source, target         *tab
	sourceRoot, targetRoot string
	paths                  []string
	dest                   string
	copying                bool
	at, moved, skipped     int
	failures               []string
}

type filesConflict struct {
	batch     *filesMoveBatch
	src, dest string
	open      bool
}

func (a *App) startFilesMove(source, target *tab, paths []string, dest string) {
	a.startFilesTransfer(source, target, paths, dest, false)
}

func (a *App) startFilesTransfer(source, target *tab, paths []string, dest string, copying bool) {
	if a.filesMoving != nil || !a.filesBulkAvailable(source) || !a.filesBulkAvailable(target) || !filesWithinRoot(target.files.root, dest) {
		return
	}
	paths = normalizeFilesSelection(paths)
	if len(paths) == 0 {
		return
	}
	batch := &filesMoveBatch{source: source, target: target, sourceRoot: source.files.root, targetRoot: target.files.root, paths: paths, dest: dest, copying: copying}
	a.filesMoving = batch
	a.nextFilesMove(batch)
}

func (a *App) nextFilesMove(b *filesMoveBatch) {
	if !a.filesBulkAvailable(b.source) || !a.filesBulkAvailable(b.target) || b.source.files.root != b.sourceRoot || b.target.files.root != b.targetRoot {
		a.filesMoving = nil
		a.filesConflict = nil
		action := "Move"
		if b.copying {
			action = "Copy"
		}
		a.notice = fmt.Sprintf("%s stopped after %d item(s): a Files folder changed.", action, b.moved)
		return
	}
	for b.at < len(b.paths) {
		src := b.paths[b.at]
		dest := filepath.Join(b.dest, filepath.Base(src))
		if err := validateFilesMove(b.sourceRoot, b.targetRoot, src, b.dest); err != nil {
			b.failures = append(b.failures, filepath.Base(src)+": "+err.Error())
			b.at++
			continue
		}
		if src == dest {
			b.skipped++
			b.at++
			continue
		}
		if _, err := os.Lstat(dest); err == nil {
			a.filesConflict = &filesConflict{batch: b, src: src, dest: dest, open: true}
			return
		} else if !os.IsNotExist(err) {
			b.failures = append(b.failures, filepath.Base(src)+": "+err.Error())
			b.at++
			continue
		}
		a.performFilesMove(b, src, dest, false)
		return
	}
	a.finishFilesMove(b)
}

func (a *App) filesConflictDialog(c *ui.Context) {
	p := a.filesConflict
	if p == nil {
		return
	}
	if !a.filesBulkAvailable(p.batch.source) || !a.filesBulkAvailable(p.batch.target) || p.batch.source.files.root != p.batch.sourceRoot || p.batch.target.files.root != p.batch.targetRoot {
		a.nextFilesMove(p.batch)
		return
	}
	choose := func(action string) {
		a.filesConflict = nil
		b := p.batch
		switch action {
		case "replace":
			a.performFilesTransfer(b, p.src, p.dest, true)
		case "both":
			to, err := availableFilesName(p.dest, isDirectory(p.src))
			if err != nil {
				b.failures = append(b.failures, err.Error())
				b.at++
				a.nextFilesMove(b)
			} else {
				a.performFilesTransfer(b, p.src, to, false)
			}
		default:
			b.skipped++
			b.at++
			a.nextFilesMove(b)
		}
	}
	ui.Modal(c.Key("files-conflict"), &p.open, func() {
		ui.Column(c).Width(410).Gap(10).Children(func() {
			ui.Text(c, "An item already exists").FontSize(16).Bold()
			ui.Text(c, fmt.Sprintf("“%s” already exists in %s. Replace moves the existing item to Trash.", filepath.Base(p.dest), filepath.Dir(p.dest))).FontSize(12)
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Skip").Clicked() {
					choose("skip")
				}
				if ui.Button(c, "Keep Both").Clicked() {
					choose("both")
				}
				if ui.Button(c, "Replace").Clicked() {
					choose("replace")
				}
			})
		})
	}).Label("File name conflict")
	if !p.open && a.filesConflict == p {
		choose("skip")
	}
}

func (a *App) performFilesMove(b *filesMoveBatch, src, dest string, replace bool) {
	a.performFilesTransfer(b, src, dest, replace)
}

func (a *App) performFilesTransfer(b *filesMoveBatch, src, dest string, replace bool) {
	sourceRoot, targetRoot, targetDir, copying := b.sourceRoot, b.targetRoot, b.dest, b.copying
	a.filesWork(func() {
		err := validateFilesMove(sourceRoot, targetRoot, src, targetDir)
		if err == nil {
			if copying {
				err = copyFilesPathTo(src, dest, replace, a.trashItem)
			} else {
				err = moveFilesPath(src, dest, replace, a.trashItem)
			}
		}
		a.filesResult(func() {
			if err != nil {
				b.failures = append(b.failures, filepath.Base(src)+": "+err.Error())
			} else {
				b.moved++
			}
			b.at++
			a.nextFilesMove(b)
		})
	})
}

func (a *App) finishFilesMove(b *filesMoveBatch) {
	a.filesMoving = nil
	if b.source.files != nil && b.source.files.root == b.sourceRoot {
		a.refreshFiles(b.source)
	}
	if b.target != b.source && b.target.files != nil && b.target.files.root == b.targetRoot {
		a.refreshFiles(b.target)
	}
	a.notice = fmt.Sprintf("%s %d item(s)", map[bool]string{true: "Copied", false: "Moved"}[b.copying], b.moved)
	if b.skipped > 0 {
		a.notice += fmt.Sprintf(", skipped %d", b.skipped)
	}
	if len(b.failures) > 0 {
		a.notice += fmt.Sprintf(", %d failed: %s", len(b.failures), b.failures[0])
	}
}

func validateFilesMove(sourceRoot, targetRoot, src, targetDir string) error {
	if src == sourceRoot || !filesWithinRoot(sourceRoot, src) {
		return errors.New("source is outside the explorer")
	}
	if !filesWithinRoot(targetRoot, targetDir) {
		return errors.New("destination is outside the explorer")
	}
	if filesWithinRoot(src, targetDir) {
		return errors.New("cannot move a folder into itself")
	}
	if err := filePathStaysInRoot(sourceRoot, src); err != nil {
		return err
	}
	if err := filePathStaysInRoot(targetRoot, targetDir); err != nil {
		return err
	}
	info, err := os.Lstat(targetDir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("destination is not a folder")
	}
	return nil
}

func availableFilesName(path string, sourceDir bool) (string, error) {
	name, ext := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), filepath.Ext(path)
	if sourceDir {
		name, ext = filepath.Base(path), ""
	}
	for n := 1; n < 10000; n++ {
		suffix := " copy"
		if n > 1 {
			suffix = fmt.Sprintf(" copy %d", n)
		}
		candidate := filepath.Join(filepath.Dir(path), name+suffix+ext)
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("too many items with this name")
}

func copyFilesPathTo(src, dest string, replace bool, trash func(string) error) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dest); err == nil {
		if !replace {
			return os.ErrExist
		}
		if trash == nil {
			return errors.New("system Trash is unavailable")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	stageDir, err := os.MkdirTemp(filepath.Dir(dest), ".larik-copy-")
	if err != nil {
		return err
	}
	preserveBackup := false
	defer func() {
		if !preserveBackup {
			_ = os.RemoveAll(stageDir)
		}
	}()
	stage := filepath.Join(stageDir, filepath.Base(dest))
	if err := copyFilesPath(src, stage, info); err != nil {
		return err
	}
	backup := filepath.Join(stageDir, "replaced")
	if _, err := os.Lstat(dest); err == nil {
		if !replace {
			return os.ErrExist
		}
		if trash == nil {
			return errors.New("system Trash is unavailable")
		}
		if err := os.Rename(dest, backup); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(stage, dest); err != nil {
		if _, backupErr := os.Lstat(backup); backupErr == nil {
			if restoreErr := os.Rename(backup, dest); restoreErr != nil {
				preserveBackup = true
				return fmt.Errorf("could not install copy: %v (backup: %s; restore: %v)", err, backup, restoreErr)
			}
		}
		return err
	}
	if _, err := os.Lstat(backup); err == nil {
		if err := trash(backup); err != nil {
			if removeErr := os.RemoveAll(dest); removeErr != nil {
				preserveBackup = true
				return fmt.Errorf("could not move replaced item to Trash: %v (new copy: %s; backup: %s; cleanup: %v)", err, dest, backup, removeErr)
			}
			if restoreErr := os.Rename(backup, dest); restoreErr != nil {
				preserveBackup = true
				return fmt.Errorf("could not move replaced item to Trash: %v (backup: %s; restore: %v)", err, backup, restoreErr)
			}
			return fmt.Errorf("could not move replaced item to Trash: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func moveFilesPath(src, dest string, replace bool, trash func(string) error) error {
	if replace && trash == nil {
		return errors.New("system Trash is unavailable")
	}
	if _, err := os.Lstat(src); err != nil {
		return err
	}
	if !replace {
		if _, err := os.Lstat(dest); err == nil {
			return os.ErrExist
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	backupDir := ""
	backup := ""
	if replace {
		if _, err := os.Lstat(dest); err == nil {
			var mkErr error
			backupDir, mkErr = os.MkdirTemp(filepath.Dir(dest), ".larik-replace-")
			if mkErr != nil {
				return mkErr
			}
			backup = filepath.Join(backupDir, filepath.Base(dest))
			if err := os.Rename(dest, backup); err != nil {
				_ = os.Remove(backupDir)
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if err := moveFilesAcrossVolumes(src, dest, trash); err != nil {
		if backup != "" {
			_ = os.Rename(backup, dest)
			_ = os.Remove(backupDir)
		}
		return err
	}
	if backup != "" {
		if err := trash(backup); err != nil {
			// The old destination remains in a named sibling folder if rollback fails.
			if undoErr := os.Rename(dest, src); undoErr == nil {
				_ = os.Rename(backup, dest)
				_ = os.Remove(backupDir)
			}
			return fmt.Errorf("could not move replaced item to Trash: %w (backup: %s)", err, backup)
		}
		_ = os.Remove(backupDir)
	}
	return nil
}

func moveFilesAcrossVolumes(src, dest string, trash func(string) error) error {
	return moveFilesAcrossVolumesUsing(src, dest, trash, os.Rename)
}

func moveFilesAcrossVolumesUsing(src, dest string, trash func(string) error, rename func(string, string) error) error {
	if err := rename(src, dest); err == nil {
		return nil
	} else if !isCrossDevice(err) {
		return err
	}
	if trash == nil {
		return errors.New("system Trash is unavailable for a cross-volume move")
	}
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	stageDir, err := os.MkdirTemp(filepath.Dir(dest), ".larik-move-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageDir)
	stage := filepath.Join(stageDir, filepath.Base(dest))
	if err := copyFilesPath(src, stage, info); err != nil {
		return err
	}
	if err := rename(stage, dest); err != nil {
		return err
	}
	if err := trash(src); err != nil {
		_ = os.RemoveAll(dest)
		return fmt.Errorf("could not move source to Trash: %w", err)
	}
	return nil
}

func isCrossDevice(err error) bool { return errors.Is(err, syscall.EXDEV) }

func (a *App) askTrashFilesEntries(tb *tab, paths []string) {
	if !a.filesBulkAvailable(tb) || len(paths) == 0 || a.filesMoving != nil {
		return
	}
	paths = normalizeFilesSelection(paths)
	root := tb.files.root
	if a.trashItem == nil {
		a.notice = "The system Trash is unavailable."
		return
	}
	a.confirmAsk(fmt.Sprintf("Move %d item(s) to the Trash?", len(paths)), "You can restore them from the system Trash until it is emptied.", "Move to Trash", func() {
		if tb.files == nil || tb.files.root != root {
			a.notice = "Files folder changed; nothing was moved to Trash."
			return
		}
		a.filesWork(func() {
			failures := []string{}
			moved := 0
			for _, path := range paths {
				if path == root || !filesWithinRoot(root, path) {
					failures = append(failures, path+": outside explorer")
					continue
				}
				if err := a.trashItem(path); err != nil {
					failures = append(failures, filepath.Base(path)+": "+err.Error())
				} else {
					moved++
				}
			}
			a.filesResult(func() {
				if tb.files != nil && tb.files.root == root {
					a.refreshFiles(tb)
					tb.files.picked = slices.DeleteFunc(tb.files.picked, func(path string) bool {
						return !slices.ContainsFunc(failures, func(f string) bool { return strings.HasPrefix(f, filepath.Base(path)+":") })
					})
				}
				a.notice = fmt.Sprintf("Moved %d item(s) to Trash", moved)
				if len(failures) > 0 {
					a.notice += fmt.Sprintf(", %d failed: %s", len(failures), failures[0])
				}
			})
		})
	})
}
