package databasepane

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/egoist/mygo"

	"larik/internal/config"
	"larik/internal/database"
)

const databaseSecretService = "Larik Desktop Database"

type Secrets interface {
	Set(string, []byte) error
	Get(string) ([]byte, error)
	Delete(string) error
}

type systemDatabaseSecrets struct{}

func (systemDatabaseSecrets) Set(key string, value []byte) error {
	return mygo.Secrets.Service(databaseSecretService).Set(key, value)
}
func (systemDatabaseSecrets) Get(key string) ([]byte, error) {
	return mygo.Secrets.Service(databaseSecretService).Get(key)
}
func (systemDatabaseSecrets) Delete(key string) error {
	return mygo.Secrets.Service(databaseSecretService).Delete(key)
}

func SecretKey(name string) string { return "connection:" + name }

// Store is shared by Desktop windows. Profiles are global to Desktop,
// never loaded from a project. The file contains no PostgreSQL URL.
type Store struct {
	mu       sync.RWMutex
	path     string
	profiles map[string]database.ConnectionConfig
	manager  *database.Manager
	secrets  Secrets
	err      error
}

func LoadStore() *Store {
	return LoadStoreWithSecrets(systemDatabaseSecrets{})
}

func LoadStoreWithSecrets(secrets Secrets) *Store {
	s := &Store{path: config.DesktopDatabasePath(), profiles: map[string]database.ConnectionConfig{}, secrets: secrets}
	data, err := os.ReadFile(s.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.err = err
	}
	if err == nil {
		var saved struct {
			Connections map[string]database.ConnectionConfig `json:"database_connections"`
		}
		if e := json.Unmarshal(data, &saved); e != nil {
			s.err = e
		} else if saved.Connections != nil {
			s.profiles = saved.Connections
		}
	}
	for name, p := range s.profiles {
		if !p.HasPassword {
			continue
		}
		secret, err := s.secrets.Get(SecretKey(name))
		if err != nil {
			p.PasswordError = "saved password is unavailable in the system credential store"
		} else {
			p.Password = string(secret)
		}
		s.profiles[name] = p
	}
	for _, p := range s.profiles {
		if p.Driver == "sqlite" && !filepath.IsAbs(p.Path) {
			s.err = errors.New("SQLite connection needs an absolute file path")
			break
		}
	}
	if s.err == nil {
		s.manager, s.err = database.New(s.profiles, "/")
	}
	return s
}

func (s *Store) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.profiles))
	for name := range s.profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (s *Store) Get(name string) (database.ConnectionConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.profiles[name]
	return p, ok
}

func (s *Store) All() map[string]database.ConnectionConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return maps.Clone(s.profiles)
}

// maskedEnv keeps database credentials out of terminals Desktop starts.
func (s *Store) MaskedEnv() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]bool{}
	var out []string
	for _, p := range s.profiles {
		for _, name := range []string{p.URLEnv, p.DSNEnv, p.PasswordEnv} {
			if name != "" && !seen[name] {
				seen[name] = true
				out = append(out, name+"=")
			}
		}
	}
	sort.Strings(out)
	return out
}

func (s *Store) Save(next map[string]database.ConnectionConfig) error {
	for _, p := range next {
		if p.Driver == "sqlite" && !filepath.IsAbs(p.Path) {
			return errors.New("SQLite connection needs an absolute file path")
		}
	}
	if _, err := database.New(next, "/"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	oldProfiles := maps.Clone(s.profiles)
	for name, p := range next {
		if p.Password != "" {
			if err := s.secrets.Set(SecretKey(name), []byte(p.Password)); err != nil {
				return errors.New("could not save password in the system credential store")
			}
			p.HasPassword = true
			p.PasswordError = ""
			next[name] = p
		}
	}
	persisted := maps.Clone(next)
	for name, p := range persisted {
		p.Password, p.PasswordError = "", ""
		persisted[name] = p
	}
	if err := config.SetSettingPath(s.path, "database_connections", persisted); err != nil {
		return err
	}
	if s.manager == nil {
		s.manager, s.err = database.New(next, "/")
		if s.err != nil {
			return s.err
		}
	} else if err := s.manager.Replace(next); err != nil {
		return err
	}
	s.profiles = maps.Clone(next)
	s.err = nil
	for name, old := range oldProfiles {
		if old.HasPassword {
			current, keep := next[name]
			if !keep || !current.HasPassword {
				if err := s.secrets.Delete(SecretKey(name)); err != nil && !errors.Is(err, mygo.ErrSecretNotFound) {
					return errors.New("connection was saved, but its old password could not be removed from the system credential store")
				}
			}
		}
	}
	return nil
}

func (s *Store) Manager() *database.Manager { s.mu.RLock(); defer s.mu.RUnlock(); return s.manager }
func (s *Store) Err() error                 { s.mu.RLock(); defer s.mu.RUnlock(); return s.err }
func (s *Store) Path() string               { return s.path }
