package schedule

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"larik/internal/filelock"
)

// Entry is one scheduled prompt.
type Entry struct {
	ID      string `json:"id"`
	Cron    string `json:"cron"`
	Prompt  string `json:"prompt"`
	Cwd     string `json:"cwd"`
	Model   string `json:"model,omitempty"`
	Mode    string `json:"mode,omitempty"`    // permission mode; default asks, so anything that would ask is denied
	Timeout string `json:"timeout,omitempty"` // per run, as for --timeout
	// Created and LastRun anchor the next run: it is the first cron time
	// after the later of the two.
	Created    time.Time `json:"created"`
	LastRun    time.Time `json:"last_run,omitzero"`
	LastStatus string    `json:"last_status,omitempty"` // "ok", or why the run failed
	LastLog    string    `json:"last_log,omitempty"`
}

// Next is when the entry runs next, or the zero time if never.
func (e Entry) Next() time.Time {
	c, err := ParseCron(e.Cron)
	if err != nil {
		return time.Time{}
	}
	from := e.Created
	if e.LastRun.After(from) {
		from = e.LastRun
	}
	return c.Next(from)
}

// Store keeps entries in one JSON file, locked while it changes, so the
// CLI and a running daemon can both update it.
type Store struct{ Path string }

// List returns the entries, oldest first.
func (s Store) List() ([]Entry, error) {
	var out []Entry
	err := s.read(&out)
	return out, err
}

func (s Store) read(out *[]Entry) error {
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s: %w", s.Path, err)
	}
	sort.SliceStable(*out, func(i, j int) bool { return (*out)[i].Created.Before((*out)[j].Created) })
	return nil
}

// update applies fn to the entries under the file's lock and saves them.
func (s Store) update(fn func([]Entry) ([]Entry, error)) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(s.Path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := filelock.Lock(lock, false); err != nil {
		return err
	}
	defer filelock.Unlock(lock)
	var entries []Entry
	if err := s.read(&entries); err != nil {
		return err
	}
	if entries, err = fn(entries); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// Add checks e and saves it with a new id, returning the saved entry.
func (s Store) Add(e Entry) (Entry, error) {
	if _, err := ParseCron(e.Cron); err != nil {
		return Entry{}, err
	}
	if strings.TrimSpace(e.Prompt) == "" {
		return Entry{}, errors.New("the prompt is empty")
	}
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	e.ID = hex.EncodeToString(b)
	if e.Created.IsZero() {
		e.Created = time.Now()
	}
	if e.Next().IsZero() {
		return Entry{}, fmt.Errorf("cron %q never matches", e.Cron)
	}
	err := s.update(func(entries []Entry) ([]Entry, error) { return append(entries, e), nil })
	return e, err
}

// Remove deletes the entry whose id starts with prefix.
func (s Store) Remove(prefix string) (Entry, error) {
	var removed Entry
	err := s.update(func(entries []Entry) ([]Entry, error) {
		i, err := find(entries, prefix)
		if err != nil {
			return nil, err
		}
		removed = entries[i]
		return append(entries[:i], entries[i+1:]...), nil
	})
	return removed, err
}

// record saves the outcome of a run of the entry with id. The entry may
// have been removed meanwhile; that isn't an error.
func (s Store) record(id string, ran time.Time, status, log string) error {
	return s.update(func(entries []Entry) ([]Entry, error) {
		for i := range entries {
			if entries[i].ID == id {
				entries[i].LastRun, entries[i].LastStatus, entries[i].LastLog = ran, status, log
			}
		}
		return entries, nil
	})
}

func find(entries []Entry, prefix string) (int, error) {
	at := -1
	for i, e := range entries {
		if prefix != "" && strings.HasPrefix(e.ID, prefix) {
			if at >= 0 {
				return 0, fmt.Errorf("%q matches more than one schedule", prefix)
			}
			at = i
		}
	}
	if at < 0 {
		return 0, fmt.Errorf("no schedule %q", prefix)
	}
	return at, nil
}
