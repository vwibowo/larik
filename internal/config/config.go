// Package config loads layered JSON settings:
//
//	~/.config/larik/config.json      user defaults
//	./.larik/settings.json           project settings (commit this)
//	./.larik/settings.local.json     personal project settings, incl. "always allow" rules
//
// Later files override scalars; permission rules accumulate.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
	cfg := &Config{Providers: map[string]ProviderConfig{}, Models: map[string]llm.ModelInfo{}}
	cfg.ConfigDir, cfg.DataDir, cfg.Cwd = configDir(), dataDir(), cwd
	for _, path := range []string{
		filepath.Join(cfg.ConfigDir, "config.json"),
		filepath.Join(cwd, ".larik", "settings.json"),
		LocalSettingsPath(cwd),
	} {
		if err := cfg.merge(path); err != nil {
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

func (c *Config) merge(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var o Config
	if err := json.Unmarshal(data, &o); err != nil {
		return fmt.Errorf("%s: %w", path, err)
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
	path := LocalSettingsPath(cwd)
	raw := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	perms, _ := raw["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
	}
	allow, _ := perms["allow"].([]any)
	for _, r := range allow {
		if r == rule {
			return nil
		}
	}
	perms["allow"] = append(allow, rule)
	raw["permissions"] = perms
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(raw, "", "  ")
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
