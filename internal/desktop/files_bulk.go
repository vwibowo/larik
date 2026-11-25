package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	filespane "larik/internal/desktop/panes/files"
)

type filesDrag struct {
	source  *tab
	paths   []string
	copying bool
}

func (a *App) filesBulkAvailable(tb *tab) bool {
	return runtime.GOOS == "darwin" && tb != nil && tb.filesState() != nil && !isRemote(tb.filesState().Root)
}

func (a *App) clickFile(tb *tab, path string, dir bool, mods ui.Modifiers) {
	change := tb.filesState().ClickPath(path, dir, mods, a.filesBulkAvailable(tb))
	if change.Apply {
		a.selectFile(tb, change.Path, change.Dir)
	}
}

func (a *App) filesRightClick(tb *tab, path string, dir bool) {
	change := tb.filesState().RightClickPath(path, dir, a.filesBulkAvailable(tb))
	if change.Apply {
		a.selectFile(tb, change.Path, change.Dir)
	}
}

func (a *App) filesDragItem(c *ui.Context, item ui.Element, tb *tab, entry fileEntry) {
	if !a.filesBulkAvailable(tb) {
		return
	}
	paths := tb.filesState().DragPaths(entry.Path)
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
	a.filesMove = &filesMovePicker{tb: tb, paths: normalizeFilesSelection(paths), dest: tb.filesState().Root, open: true}
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
	filespane.MoveDialog(c, filespane.MoveDialogOptions{
		Open: &p.open, Count: len(p.paths), Dest: &p.dest, State: p.tb.filesState(),
		LoadDir: func(dir string) { a.loadFilesDir(p.tb, dir) },
		Cancel:  func() { a.filesMove = nil },
		Move: func() {
			a.filesMove = nil
			a.startFilesMove(p.tb, p.tb, p.paths, p.dest)
		},
	})
	if !p.open && a.filesMove == p {
		a.filesMove = nil
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
	if a.filesMoving != nil || !a.filesBulkAvailable(source) || !a.filesBulkAvailable(target) || !filesWithinRoot(target.filesState().Root, dest) {
		return
	}
	paths = normalizeFilesSelection(paths)
	if len(paths) == 0 {
		return
	}
	batch := &filesMoveBatch{source: source, target: target, sourceRoot: source.filesState().Root, targetRoot: target.filesState().Root, paths: paths, dest: dest, copying: copying}
	a.filesMoving = batch
	a.nextFilesMove(batch)
}

func (a *App) nextFilesMove(b *filesMoveBatch) {
	if !a.filesBulkAvailable(b.source) || !a.filesBulkAvailable(b.target) || b.source.filesState().Root != b.sourceRoot || b.target.filesState().Root != b.targetRoot {
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
	if !a.filesBulkAvailable(p.batch.source) || !a.filesBulkAvailable(p.batch.target) || p.batch.source.filesState().Root != p.batch.sourceRoot || p.batch.target.filesState().Root != p.batch.targetRoot {
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
	filespane.ConflictDialog(c, filespane.ConflictDialogOptions{Open: &p.open, Dest: p.dest, Choose: choose})
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
	if b.source.filesState() != nil && b.source.filesState().Root == b.sourceRoot {
		a.refreshFiles(b.source)
	}
	if b.target != b.source && b.target.filesState() != nil && b.target.filesState().Root == b.targetRoot {
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
	return filespane.ValidateMove(sourceRoot, targetRoot, src, targetDir, filePathStaysInRoot)
}
func copyFilesPathTo(src, dest string, replace bool, trash func(string) error) error {
	return filespane.CopyPathTo(src, dest, replace, trash)
}
func moveFilesPath(src, dest string, replace bool, trash func(string) error) error {
	return filespane.MovePath(src, dest, replace, trash)
}
func moveFilesAcrossVolumes(src, dest string, trash func(string) error) error {
	return filespane.MoveAcrossVolumes(src, dest, trash)
}
func moveFilesAcrossVolumesUsing(src, dest string, trash func(string) error, rename func(string, string) error) error {
	return filespane.MoveAcrossVolumesUsing(src, dest, trash, rename)
}
func isCrossDevice(err error) bool { return filespane.IsCrossDevice(err) }

func (a *App) askTrashFilesEntries(tb *tab, paths []string) {
	if !a.filesBulkAvailable(tb) || len(paths) == 0 || a.filesMoving != nil {
		return
	}
	paths = normalizeFilesSelection(paths)
	root := tb.filesState().Root
	if a.trashItem == nil {
		a.notice = "The system Trash is unavailable."
		return
	}
	a.confirmAsk(fmt.Sprintf("Move %d item(s) to the Trash?", len(paths)), "You can restore them from the system Trash until it is emptied.", "Move to Trash", func() {
		if tb.filesState() == nil || tb.filesState().Root != root {
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
				if tb.filesState() != nil && tb.filesState().Root == root {
					a.refreshFiles(tb)
					tb.filesState().Picked = slices.DeleteFunc(tb.filesState().Picked, func(path string) bool {
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

func availableFilesName(path string, sourceDir bool) (string, error) {
	return filespane.AvailableName(path, sourceDir)
}
