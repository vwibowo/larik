// Package config loads layered JSON settings:
//
//	~/.config/larik/config.json      user defaults
//	./.larik/settings.json           project settings (commit this)
//	./.larik/settings.local.json     personal project settings, incl. "always allow" rules
//
// Later files override scalars; permission rules accumulate. MCP servers
// are also read from the project's .mcp.json (see mcp.go).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"larik/internal/hooks"
	"larik/internal/llm"
	"larik/internal/permission"
)

type ProviderConfig struct {
	// Type selects the adapter: anthropic, openai, gemini, or openai-compatible.
	// It defaults to the provider's name for the built-in providers.
	Type      string `json:"type,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
	APIKey    string `json:"api_key,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
}

type Config struct {
	Model       string                    `json:"model,omitempty"` // "provider/model" or bare model id
	Effort      llm.Effort                `json:"effort,omitempty"`
	Mode        permission.Mode           `json:"mode,omitempty"`
	MaxTurns    int                       `json:"max_turns,omitempty"`
	Providers   map[string]ProviderConfig `json:"providers,omitempty"`
	Models      map[string]llm.ModelInfo  `json:"models,omitempty"` // catalog additions/overrides
	Permissions permission.Rules          `json:"permissions,omitempty"`
	MCPServers  map[string]MCPServer      `json:"mcp_servers,omitempty"`
	// ApprovedMCP maps project-scoped server names to the hash of the config
	// the user approved. Only honored from settings.local.json.
	ApprovedMCP map[string]string `json:"approved_mcp_servers,omitempty"`
	Hooks       hooks.Config      `json:"hooks,omitempty"`
	// ApprovedHooks is the hash of the approved project hook set (from
	// .larik/settings.json). Only honored from settings.local.json.
	ApprovedHooks string `json:"approved_project_hooks,omitempty"`

	// TrustedHooks come from personal files; ProjectHooks from shared ones.
	TrustedHooks hooks.Config `json:"-"`
	ProjectHooks hooks.Config `json:"-"`

	// Resolved paths, not serialized.
	ConfigDir string `json:"-"`
	DataDir   string `json:"-"`
	Cwd       string `json:"-"`
}

func configDir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "larik")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "larik")
}

func dataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "larik")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "larik")
}

// LocalSettingsPath is where "always allow" rules are persisted.
func LocalSettingsPath(cwd string) string {
	return filepath.Join(cwd, ".larik", "settings.local.json")
}

func Load(cwd string) (*Config, error) {
	cfg := &Config{
		Providers:   map[string]ProviderConfig{},
		Models:      map[string]llm.ModelInfo{},
		MCPServers:  map[string]MCPServer{},
		ApprovedMCP: map[string]string{},
	}
	cfg.ConfigDir, cfg.DataDir, cfg.Cwd = configDir(), dataDir(), cwd
	layers := []struct {
		path    string
		trusted bool // servers from this file start without approval
	}{
		{filepath.Join(cfg.ConfigDir, "config.json"), true},
		{filepath.Join(cwd, ".mcp.json"), false},
		{filepath.Join(cwd, ".larik", "settings.json"), false},
		{LocalSettingsPath(cwd), true},
	}
	for _, l := range layers {
		if err := cfg.merge(l.path, l.trusted); err != nil {
			return nil, err
		}
	}
	for id, m := range cfg.Models {
		m.ID = id
		llm.Catalog[id] = m
	}
	if cfg.MaxTurns == 0 {
		cfg.MaxTurns = 200
	}
	return cfg, nil
}

func (c *Config) merge(path string, trusted bool) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var o struct {
		Config
		MCPServersCompat map[string]MCPServer `json:"mcpServers"` // .mcp.json / Claude Code format
	}
	if err := json.Unmarshal(data, &o); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for _, servers := range []map[string]MCPServer{o.MCPServersCompat, o.MCPServers} {
		for name, srv := range servers {
			srv.Name, srv.Source, srv.Trusted = name, path, trusted
			c.MCPServers[name] = srv
		}
	}
	if trusted {
		c.TrustedHooks = c.TrustedHooks.Merge(o.Hooks)
	} else {
		c.ProjectHooks = c.ProjectHooks.Merge(o.Hooks)
	}
	if filepath.Base(path) == "settings.local.json" {
		for k, v := range o.ApprovedMCP {
			c.ApprovedMCP[k] = v
		}
		if o.ApprovedHooks != "" {
			c.ApprovedHooks = o.ApprovedHooks
		}
	}
	if o.Model != "" {
		c.Model = o.Model
	}
	if o.Effort != "" {
		c.Effort = o.Effort
	}
	if o.Mode != "" {
		c.Mode = o.Mode
	}
	if o.MaxTurns != 0 {
		c.MaxTurns = o.MaxTurns
	}
	for k, v := range o.Providers {
		c.Providers[k] = v
	}
	for k, v := range o.Models {
		c.Models[k] = v
	}
	c.Permissions.Allow = append(c.Permissions.Allow, o.Permissions.Allow...)
	c.Permissions.Deny = append(c.Permissions.Deny, o.Permissions.Deny...)
	return nil
}

// PersistAllowRule appends an allow rule to the project's local settings.
func PersistAllowRule(cwd, rule string) error {
	return updateLocal(cwd, func(raw map[string]any) {
		perms, _ := raw["permissions"].(map[string]any)
		if perms == nil {
			perms = map[string]any{}
		}
		allow, _ := perms["allow"].([]any)
		for _, r := range allow {
			if r == rule {
				return
			}
		}
		perms["allow"] = append(allow, rule)
		raw["permissions"] = perms
	})
}

// updateLocal edits settings.local.json as generic JSON so unknown keys survive.
func updateLocal(cwd string, edit func(raw map[string]any)) error {
	path := LocalSettingsPath(cwd)
	raw := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	edit(raw)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(raw, "", "  ")
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// ProjectHooksApproved reports whether shared project hooks may run.
func (c *Config) ProjectHooksApproved() bool {
	return c.ProjectHooks.Empty() || c.ApprovedHooks == c.ProjectHooks.Hash()
}

// ActiveHooks returns the hooks that may run: personal hooks always,
// project hooks only once approved.
func (c *Config) ActiveHooks() hooks.Config {
	if c.ProjectHooksApproved() {
		return c.TrustedHooks.Merge(c.ProjectHooks)
	}
	return c.TrustedHooks
}

// ApproveProjectHooks records approval of the current project hook set.
func (c *Config) ApproveProjectHooks() error {
	hash := c.ProjectHooks.Hash()
	if err := updateLocal(c.Cwd, func(raw map[string]any) { raw["approved_project_hooks"] = hash }); err != nil {
		return err
	}
	c.ApprovedHooks = hash
	return nil
}
