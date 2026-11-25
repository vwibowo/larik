package filespane

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func WithinRoot(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func CopyPath(from, to string, info os.FileInfo) error {
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
			if err := CopyPath(src, filepath.Join(to, entry.Name()), childInfo); err != nil {
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

func ValidateMove(sourceRoot, targetRoot, src, targetDir string, stayInRoot func(string, string) error) error {
	if src == sourceRoot || !WithinRoot(sourceRoot, src) {
		return errors.New("source is outside the explorer")
	}
	if !WithinRoot(targetRoot, targetDir) {
		return errors.New("destination is outside the explorer")
	}
	if WithinRoot(src, targetDir) {
		return errors.New("cannot move a folder into itself")
	}
	if err := stayInRoot(sourceRoot, src); err != nil {
		return err
	}
	if err := stayInRoot(targetRoot, targetDir); err != nil {
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

func CopyPathTo(src, dest string, replace bool, trash func(string) error) error {
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
	if err := CopyPath(src, stage, info); err != nil {
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

func MovePath(src, dest string, replace bool, trash func(string) error) error {
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
	if err := MoveAcrossVolumes(src, dest, trash); err != nil {
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

func MoveAcrossVolumes(src, dest string, trash func(string) error) error {
	return MoveAcrossVolumesUsing(src, dest, trash, os.Rename)
}

func MoveAcrossVolumesUsing(src, dest string, trash func(string) error, rename func(string, string) error) error {
	if err := rename(src, dest); err == nil {
		return nil
	} else if !IsCrossDevice(err) {
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
	if err := CopyPath(src, stage, info); err != nil {
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

func IsCrossDevice(err error) bool { return errors.Is(err, syscall.EXDEV) }

func IsDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

func DuplicatePath(path string) (string, error) {
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
		if err := CopyPath(path, to, info); err != nil {
			if !os.IsExist(err) {
				_ = os.RemoveAll(to)
			}
			return "", err
		}
		return to, nil
	}
	return "", fmt.Errorf("too many copies already exist")
}
