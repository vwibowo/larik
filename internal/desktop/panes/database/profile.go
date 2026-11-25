package databasepane

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"larik/internal/database"
)

type ProfileStore interface {
	Get(string) (database.ConnectionConfig, bool)
	All() map[string]database.ConnectionConfig
	Save(map[string]database.ConnectionConfig) error
}

func BeginProfile(d *State, name string, store ProfileStore) {
	d.Editing, d.Deleting, d.OldName, d.DraftName = true, false, name, name
	d.DraftDriver, d.DraftValue, d.DraftAgent, d.Status = "sqlite", "", false, ""
	d.DraftHost, d.DraftPort, d.DraftDatabase, d.DraftUser, d.DraftTrust = "", "1433", "", "", false
	d.DraftPasswordEnv, d.DraftPassword, d.DraftSSLMode, d.DraftClearPassword = "", "", "prefer", false
	if p, ok := store.Get(name); ok {
		d.DraftDriver, d.DraftAgent = p.Driver, p.AgentAccess
		switch p.Driver {
		case "sqlite":
			d.DraftValue = p.Path
		case "postgres":
			d.DraftPasswordEnv = p.PasswordEnv
			d.DraftSSLMode = p.SSLMode
			if d.DraftSSLMode == "" {
				d.DraftSSLMode = "prefer"
			}
			if p.URLEnv == "" {
				d.DraftHost, d.DraftPort, d.DraftDatabase, d.DraftUser = p.Host, strconv.Itoa(p.Port), p.Database, p.User
			} else {
				d.DraftValue = p.URLEnv // legacy environment-backed URL
			}
		case "mysql":
			d.DraftValue = p.DSNEnv
			d.DraftHost, d.DraftPort, d.DraftDatabase, d.DraftUser = p.Host, strconv.Itoa(p.Port), p.Database, p.User
			d.DraftPasswordEnv, d.DraftSSLMode = p.PasswordEnv, p.SSLMode
			if d.DraftSSLMode == "" {
				d.DraftSSLMode = "required"
			}
		case "sqlserver":
			d.DraftPasswordEnv = p.PasswordEnv
			d.DraftHost, d.DraftPort, d.DraftDatabase, d.DraftUser, d.DraftTrust = p.Host, strconv.Itoa(p.Port), p.Database, p.User, p.TrustServerCertificate
		}
	}
}

func DraftConfig(d *State, store ProfileStore) (database.ConnectionConfig, error) {
	p := database.ConnectionConfig{Driver: d.DraftDriver, AgentAccess: d.DraftAgent}
	value := strings.TrimSpace(d.DraftValue)
	switch p.Driver {
	case "sqlite":
		p.Path = value
	case "postgres":
		if value != "" { // preserve an existing URL-based profile
			p.URLEnv = value
			break
		}
		port, err := strconv.Atoi(strings.TrimSpace(d.DraftPort))
		if err != nil {
			return p, fmt.Errorf("enter a valid PostgreSQL port")
		}
		p.Host, p.Port, p.Database, p.User, p.SSLMode = strings.TrimSpace(d.DraftHost), port, strings.TrimSpace(d.DraftDatabase), strings.TrimSpace(d.DraftUser), d.DraftSSLMode
		applyDraftPassword(d, &p, store)
	case "mysql":
		if value != "" { // legacy DSN profiles remain supported
			p.DSNEnv = value
			break
		}
		port, err := strconv.Atoi(strings.TrimSpace(d.DraftPort))
		if err != nil {
			return p, fmt.Errorf("enter a valid MySQL port")
		}
		p.Host, p.Port, p.Database, p.User, p.SSLMode = strings.TrimSpace(d.DraftHost), port, strings.TrimSpace(d.DraftDatabase), strings.TrimSpace(d.DraftUser), d.DraftSSLMode
		applyDraftPassword(d, &p, store)
	case "sqlserver":
		port, err := strconv.Atoi(strings.TrimSpace(d.DraftPort))
		if err != nil {
			return p, fmt.Errorf("enter a valid SQL Server port")
		}
		p.Host, p.Port, p.Database, p.User, p.TrustServerCertificate = strings.TrimSpace(d.DraftHost), port, strings.TrimSpace(d.DraftDatabase), strings.TrimSpace(d.DraftUser), d.DraftTrust
		p.PasswordEnv = strings.TrimSpace(d.DraftPasswordEnv)
		applyDraftPassword(d, &p, store)
	}
	if _, err := database.New(map[string]database.ConnectionConfig{"connection-test": p}, "/"); err != nil {
		return p, err
	}
	return p, nil
}

func applyDraftPassword(d *State, p *database.ConnectionConfig, store ProfileStore) {
	if d.DraftClearPassword {
		p.Password, p.PasswordError, p.HasPassword = "", "", false
	}
	if d.DraftPassword != "" {
		p.Password, p.PasswordError, p.HasPassword, p.PasswordEnv = d.DraftPassword, "", true, ""
		return
	}
	if p.PasswordEnv != "" {
		p.Password, p.PasswordError, p.HasPassword = "", "", false
		return
	}
	if !d.DraftClearPassword && d.OldName != "" {
		if existing, ok := store.Get(d.OldName); ok && existing.HasPassword {
			p.Password, p.PasswordError, p.HasPassword = existing.Password, existing.PasswordError, true
		}
	}
}

func SaveProfile(d *State, store ProfileStore) (name, oldName string, saved bool) {
	name = strings.TrimSpace(d.DraftName)
	if name == "" {
		d.Status = "Connection name is required"
		return "", "", false
	}
	next := store.All()
	if _, exists := next[name]; exists && name != d.OldName {
		d.Status = "Connection name already exists"
		return "", "", false
	}
	oldName = d.OldName
	delete(next, oldName)
	p, err := DraftConfig(d, store)
	if err != nil {
		d.Status = err.Error()
		return "", "", false
	}
	next[name] = p
	if err := store.Save(next); err != nil {
		d.Status = err.Error()
		return "", "", false
	}
	d.Editing, d.Profile, d.Driver, d.Status = false, name, p.Driver, ""
	d.DraftPassword = ""
	return name, oldName, true
}

func DeleteProfile(name string, store ProfileStore) error {
	next := store.All()
	delete(next, name)
	return store.Save(next)
}

func TestConnection(d *State, store ProfileStore, work, update func(func()), stillOpen func() bool) {
	p, err := DraftConfig(d, store)
	if err != nil {
		d.Status = err.Error()
		return
	}
	d.TestingConnection, d.Status = true, "Testing connection…"
	work(func() {
		manager, err := database.New(map[string]database.ConnectionConfig{"connection-test": p}, "/")
		if err == nil {
			_, err = manager.Schema(context.Background(), "connection-test", false)
		}
		if err != nil {
			err = fmt.Errorf("Connection failed: %w", err)
		}
		update(func() {
			if !stillOpen() || d.Ctx.Err() != nil {
				return
			}
			d.TestingConnection = false
			if err != nil {
				d.Status = err.Error()
			} else {
				d.Status = "Connection successful"
			}
		})
	})
}
