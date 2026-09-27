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
	"strings"

	"larik/internal/hooks"
	"larik/internal/llm"
	"larik/internal/lsp"
	"larik/internal/permission"
	"larik/internal/sandbox"
	"larik/internal/web"
)

type ProviderConfig struct {
	// Type selects the adapter: anthropic, openai, gemini, or openai-compatible.
	// It defaults to the provider's name for the built-in providers.
	Type      string `json:"type,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
	APIKey    string `json:"api_key,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
	// ContextLength is the context window Ollama loads models with
	// (num_ctx). Zero uses 32768, capped at the model's maximum.
	ContextLength int `json:"context_length,omitempty"`
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

	// Web configures web_fetch and web_search. The search backend (which
	// receives every query) is honored only from personal files.
	Web WebConfig `json:"web,omitempty"`

	// Sandbox configures the OS sandbox for bash. Shared project files may
	// only tighten it (enable it); loosening needs a personal file.
	Sandbox sandbox.Config `json:"sandbox,omitempty"`

	// LSP configures language servers. Shared project files may only
	// disable servers; commands are honored from personal files only.
	LSP map[string]lsp.ServerConfig `json:"lsp,omitempty"`

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
		if o.Web.Search != (web.SearchConfig{}) {
			c.Web.Search = o.Web.Search
		}
		if o.Web.FetchDisabled {
			c.Web.FetchDisabled = true
		}
	} else {
		// Shared files may only turn web tools off.
		if o.Web.FetchDisabled {
			c.Web.FetchDisabled = true
		}
		if o.Web.Search.Disabled {
			c.Web.Search.Disabled = true
		}
	}
	if trusted {
		if o.Sandbox.Enabled != nil {
			c.Sandbox.Enabled = o.Sandbox.Enabled
		}
		if o.Sandbox.Network {
			c.Sandbox.Network = true
		}
		c.Sandbox.Writable = append(c.Sandbox.Writable, o.Sandbox.Writable...)
	} else if o.Sandbox.Enabled != nil && *o.Sandbox.Enabled {
		c.Sandbox.Enabled = o.Sandbox.Enabled // a shared file may only switch it on
	}
	for name, srv := range o.LSP {
		if c.LSP == nil {
			c.LSP = map[string]lsp.ServerConfig{}
		}
		if !trusted {
			// A shared file can't make Larik run a new command.
			prev := c.LSP[name]
			prev.Disabled = srv.Disabled
			c.LSP[name] = prev
			continue
		}
		c.LSP[name] = srv
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
	return updateJSON(LocalSettingsPath(cwd), 0o644, edit)
}

// updateJSON edits a settings file as generic JSON so unknown keys survive.
// A file that exists keeps its permissions, unless perm is private (no
// group or other access), which is then enforced.
func updateJSON(path string, perm os.FileMode, edit func(raw map[string]any)) error {
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
	if fi, err := os.Stat(path); err == nil {
		private := perm&0o077 == 0
		perm = fi.Mode().Perm()
		if private {
			perm &^= 0o077
		}
	}
	b, _ := json.MarshalIndent(raw, "", "  ")
	if err := os.WriteFile(path, append(b, '\n'), perm); err != nil {
		return err
	}
	return os.Chmod(path, perm) // WriteFile keeps an existing file's mode
}

// UserConfigPath is the personal config file shared by all projects.
func (c *Config) UserConfigPath() string {
	return filepath.Join(c.ConfigDir, "config.json")
}

// SaveProvider records a provider in the settings file at path (the user
// config or LocalSettingsPath) and, when model is set, makes model the
// default. A zero pc writes no provider entry. It also updates c.
func (c *Config) SaveProvider(path, name string, pc ProviderConfig, model string) error {
	perm := os.FileMode(0o644)
	if pc.APIKey != "" {
		perm = 0o600 // the file now holds a secret
	}
	err := updateJSON(path, perm, func(raw map[string]any) {
		if pc != (ProviderConfig{}) {
			ps, _ := raw["providers"].(map[string]any)
			if ps == nil {
				ps = map[string]any{}
			}
			var entry map[string]any
			b, _ := json.Marshal(pc)
			_ = json.Unmarshal(b, &entry)
			ps[name] = entry
			raw["providers"] = ps
		}
		if model != "" {
			raw["model"] = model
		}
	})
	if err != nil {
		return err
	}
	if pc != (ProviderConfig{}) {
		c.Providers[name] = pc
	}
	if model != "" {
		c.Model = model
	}
	return nil
}

// RemoveProvider deletes a provider from the personal settings files (the
// user config and .larik/settings.local.json), along with a default model
// in the same file that uses it. It returns the files it changed. Shared
// project settings are left alone; see ProviderInShared.
func (c *Config) RemoveProvider(name string) ([]string, error) {
	var changed []string
	for _, path := range []string{c.UserConfigPath(), LocalSettingsPath(c.Cwd)} {
		raw := map[string]any{}
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return changed, err
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return changed, fmt.Errorf("%s: %w", path, err)
		}
		ps, _ := raw["providers"].(map[string]any)
		model, _ := raw["model"].(string)
		_, has := ps[name]
		usesIt := strings.HasPrefix(model, name+"/")
		if !has && !usesIt {
			continue
		}
		err = updateJSON(path, 0o644, func(raw map[string]any) {
			if ps, _ := raw["providers"].(map[string]any); ps != nil {
				delete(ps, name)
				if len(ps) == 0 {
					delete(raw, "providers")
				}
			}
			if usesIt {
				delete(raw, "model")
			}
		})
		if err != nil {
			return changed, err
		}
		changed = append(changed, path)
	}
	delete(c.Providers, name)
	if strings.HasPrefix(c.Model, name+"/") {
		c.Model = ""
	}
	return changed, nil
}

// ProviderInShared reports whether the shared project settings
// (.larik/settings.json) define a provider, which only editing that file
// can remove.
func (c *Config) ProviderInShared(name string) bool {
	data, err := os.ReadFile(filepath.Join(c.Cwd, ".larik", "settings.json"))
	if err != nil {
		return false
	}
	var raw struct {
		Providers map[string]json.RawMessage `json:"providers"`
	}
	_ = json.Unmarshal(data, &raw)
	_, ok := raw.Providers[name]
	return ok
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

// WebConfig is the "web" settings section.
type WebConfig struct {
	FetchDisabled bool             `json:"fetch_disabled,omitempty"`
	Search        web.SearchConfig `json:"search,omitempty"`
}
