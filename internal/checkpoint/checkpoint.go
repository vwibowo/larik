// Package checkpoint snapshots files before the agent modifies them so a
// turn's changes can be undone.
package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type snapshot struct {
	Path    string `json:"path"`
	Existed bool   `json:"existed"`
	Blob    string `json:"blob,omitempty"` // file under the turn dir holding the original bytes
	Mode    uint32 `json:"mode,omitempty"`
}

type turn struct {
	N     int        `json:"n"`
	Files []snapshot `json:"files"`
}

// Store keeps one snapshot per file per turn, taken at the first write.
type Store struct {
	dir string

	mu    sync.Mutex
	turns []*turn
	seen  map[string]bool // paths captured in the current turn
}

func New(dir string) *Store { return &Store{dir: dir} }

// BeginTurn starts a new undo unit. Each user prompt is one turn.
func (s *Store) BeginTurn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turns = append(s.turns, &turn{N: len(s.turns) + 1})
	s.seen = map[string]bool{}
}

// Capture records a file's current state if not already captured this turn.
func (s *Store) Capture(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.turns) == 0 {
		s.turns = append(s.turns, &turn{N: 1})
		s.seen = map[string]bool{}
	}
	if s.seen[path] {
		return nil
	}
	s.seen[path] = true
	t := s.turns[len(s.turns)-1]
	snap := snapshot{Path: path}
	fi, err := os.Stat(path)
	if err == nil {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		tdir := filepath.Join(s.dir, fmt.Sprint(t.N))
		if err := os.MkdirAll(tdir, 0o700); err != nil {
			return err
		}
		snap.Existed = true
		snap.Mode = uint32(fi.Mode().Perm())
		snap.Blob = filepath.Join(tdir, fmt.Sprintf("%d.bin", len(t.Files)))
		if err := os.WriteFile(snap.Blob, data, 0o600); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	t.Files = append(t.Files, snap)
	return s.saveManifest(t)
}

func (s *Store) saveManifest(t *turn) error {
	tdir := filepath.Join(s.dir, fmt.Sprint(t.N))
	if err := os.MkdirAll(tdir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(t, "", "  ")
	return os.WriteFile(filepath.Join(tdir, "manifest.json"), b, 0o600)
}

// Undo reverts the most recent turn that changed files and returns the
// restored paths.
func (s *Store) Undo() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.turns) > 0 {
		t := s.turns[len(s.turns)-1]
		s.turns = s.turns[:len(s.turns)-1]
		s.seen = map[string]bool{}
		if len(t.Files) == 0 {
			continue
		}
		var restored []string
		for i := len(t.Files) - 1; i >= 0; i-- {
			f := t.Files[i]
			if !f.Existed {
				if err := os.Remove(f.Path); err != nil && !os.IsNotExist(err) {
					return restored, err
				}
			} else {
				data, err := os.ReadFile(f.Blob)
				if err != nil {
					return restored, err
				}
				if err := os.WriteFile(f.Path, data, os.FileMode(f.Mode)); err != nil {
					return restored, err
				}
			}
			restored = append(restored, f.Path)
		}
		_ = os.RemoveAll(filepath.Join(s.dir, fmt.Sprint(t.N)))
		return restored, nil
	}
	return nil, fmt.Errorf("nothing to undo")
}
