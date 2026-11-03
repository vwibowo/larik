package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"larik/internal/status"
)

// A session's name, archive and end: a name given to it, shown wherever
// sessions are listed instead of its first prompt, is kept in a file of
// its own beside the transcript (NamePath), which a running larik is
// appending to. Archiving moves the session's files, the transcript and
// those beside it, into the archive folder of its sessions folder, where
// List doesn't look and Archived does; Gather puts them in a folder of
// their own, for the Trash. A session a process has open stays.

// archiveDir is the folder archived sessions are moved to, in a sessions
// folder.
const archiveDir = "archive"

// NamePath is the file holding the name given to the session at
// sessionPath.
func NamePath(sessionPath string) string { return strings.TrimSuffix(sessionPath, ".jsonl") + ".name" }

// maxName bounds a name, as a title is.
const maxName = 120

// SetName names the session at sessionPath, or with a blank name, takes
// its name away. Spaces run together, as a title's.
func SetName(sessionPath, name string) error {
	name = strings.Join(strings.Fields(name), " ")
	if len(name) > maxName {
		name = name[:maxName]
	}
	if name == "" {
		if err := os.Remove(NamePath(sessionPath)); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	tmp := NamePath(sessionPath) + ".tmp"
	if err := os.WriteFile(tmp, []byte(name+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, NamePath(sessionPath))
}

// readName is the name in a session's name file, or "".
func readName(sessionPath string) string {
	data, err := os.ReadFile(NamePath(sessionPath))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ErrRunning says a process has the session open.
var ErrRunning = errors.New("the session is running: end it first")

// files are the names, in its folder, of the session at sessionPath's
// files: the transcript and those beside it, its id then a dot.
func files(sessionPath string) ([]string, error) {
	dir, base := filepath.Split(sessionPath)
	id := strings.TrimSuffix(base, ".jsonl")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), id+".") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// moveSession moves the session at sessionPath's files to the folder to,
// refusing while it runs, and returns its transcript's path there. What
// moved goes back when one can't.
func moveSession(sessionPath, to string) (string, error) {
	if f, ok := status.Read(sessionPath); ok && f.Running() {
		return "", ErrRunning
	}
	if _, err := os.Stat(sessionPath); err != nil {
		return "", err
	}
	names, err := files(sessionPath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(to, 0o700); err != nil {
		return "", err
	}
	from := filepath.Dir(sessionPath)
	for _, n := range names {
		if _, err := os.Lstat(filepath.Join(to, n)); err == nil {
			return "", fmt.Errorf("%s is there already", filepath.Join(to, n))
		}
	}
	for i, n := range names {
		if err := os.Rename(filepath.Join(from, n), filepath.Join(to, n)); err != nil {
			for _, back := range names[:i] {
				_ = os.Rename(filepath.Join(to, back), filepath.Join(from, back))
			}
			return "", err
		}
	}
	scanMu.Lock()
	delete(scanCache, sessionPath)
	scanMu.Unlock()
	return filepath.Join(to, filepath.Base(sessionPath)), nil
}

// Archive moves the session at sessionPath to its folder's archive, and
// returns where its transcript is now.
func Archive(sessionPath string) (string, error) {
	if IsArchived(sessionPath) {
		return sessionPath, nil
	}
	return moveSession(sessionPath, filepath.Join(filepath.Dir(sessionPath), archiveDir))
}

// Unarchive moves an archived session back among its folder's sessions,
// and returns where its transcript is now.
func Unarchive(sessionPath string) (string, error) {
	if !IsArchived(sessionPath) {
		return sessionPath, nil
	}
	return moveSession(sessionPath, filepath.Dir(filepath.Dir(sessionPath)))
}

// IsArchived reports whether the transcript at sessionPath is archived.
func IsArchived(sessionPath string) bool {
	return filepath.Base(filepath.Dir(sessionPath)) == archiveDir
}

// Archived lists the archived sessions of the sessions folder dir, newest
// first.
func Archived(dir string) ([]Info, error) { return List(filepath.Join(dir, archiveDir)) }

// Gather moves the session at sessionPath's files into a new folder in
// its folder, named for it, for the Trash, and returns the folder;
// Ungather puts them back.
func Gather(sessionPath string) (string, error) {
	id := strings.TrimSuffix(filepath.Base(sessionPath), ".jsonl")
	folder := filepath.Join(filepath.Dir(sessionPath), "larik session "+id)
	if _, err := moveSession(sessionPath, folder); err != nil {
		os.Remove(folder) // empty, when nothing moved
		return "", err
	}
	return folder, nil
}

// Ungather puts back the files Gather moved to folder, as the Trash
// refused them, and removes the folder.
func Ungather(sessionPath, folder string) error {
	id := strings.TrimSuffix(filepath.Base(sessionPath), ".jsonl")
	if _, err := moveSession(filepath.Join(folder, id+".jsonl"), filepath.Dir(sessionPath)); err != nil {
		return err
	}
	return os.Remove(folder)
}

// Resume finds the session id names in dir, as Find does, and brings it
// back from the archive if it's there, as opening an archived one in
// larik desktop does.
func Resume(dir, id string) (string, error) {
	path, err := Find(dir, id)
	if err == nil {
		return path, nil
	}
	archived, aerr := Find(filepath.Join(dir, archiveDir), id)
	if aerr != nil {
		return "", err
	}
	return Unarchive(archived)
}
