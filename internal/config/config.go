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
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

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
	Model    string          `json:"model,omitempty"` // "provider/model" or bare model id
	Effort   llm.Effort      `json:"effort,omitempty"`
	Mode     permission.Mode `json:"mode,omitempty"`
	MaxTurns int             `json:"max_turns,omitempty"`
	// CheckpointRetentionDays is how long /undo snapshots are kept: 0
	// means the default (7), a negative value keeps them forever. The
	// cleanup covers every project, so only personal files may set it.
	CheckpointRetentionDays int `json:"checkpoint_retention_days,omitempty"`
	// Theme picks the TUI colors: "auto" (follow the terminal's
	// background, the default), "dark" or "light".
	Theme string `json:"theme,omitempty"`
	// AutoCompact summarizes the conversation when the context is nearly
	// full. Nil means on.
	AutoCompact *bool `json:"auto_compact,omitempty"`
	// Verbose shows tool output and thinking in full in the TUI.
	Verbose *bool `json:"verbose,omitempty"`
	// Notifications alerts an unfocused terminal when larik needs you:
	// "off" (the default), "bell" or "desktop".
	Notifications string `json:"notifications,omitempty"`
	// Language is what the model replies in; empty leaves it to the model.
	Language string `json:"language,omitempty"`
	// SpinnerTips shows a tip under the spinner during a turn. Nil means on.
	SpinnerTips *bool `json:"spinner_tips,omitempty"`

	// Roles name models by job, so subagents and compaction can run on a
	// cheaper model than the main agent: "worker", "explore", "smart",
	// "compact" or any name of your own, each a "provider/model" spec.
	// An empty or missing role inherits the main model.
	Roles map[string]string `json:"roles,omitempty"`
	// Fallbacks lists, per role name or model spec, models to switch to
	// when the primary fails before answering (rate limit, quota, outage).
	Fallbacks map[string][]string `json:"fallbacks,omitempty"`
	// RoleOptions limit subagents running on a role: a worktree to keep a
	// cheap model's edits off your checkout, and a turn cap.
	RoleOptions map[string]RoleOption `json:"role_options,omitempty"`
	// Budget caps a session's spend.
	Budget Budget `json:"budget,omitempty"`

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

// RoleOption applies to subagents that run on a role.
type RoleOption struct {
	// Isolation "worktree" runs them in their own git worktree, so their
	// changes come back as a branch to review.
	Isolation string `json:"isolation,omitempty"`
	// MaxTurns caps their model requests; zero keeps the default (100).
	MaxTurns int `json:"max_turns,omitempty"`
	// Context "minimal" drops the global instructions and skills index
	// from their prompt, keeping only this project's own instructions.
	// Smaller and more focused for a small model; zero value ("") keeps
	// the full context.
	Context string `json:"context,omitempty"`
}

// Budget caps what a session may spend, subagents included.
type Budget struct {
	// SessionUSD stops the agent before a request once the session's
	// cost reaches it. Zero means no cap.
	SessionUSD float64 `json:"session_usd,omitempty"`
	// WarnAt is the fraction of SessionUSD at which to warn (default 0.8).
	WarnAt float64 `json:"warn_at,omitempty"`
}

// WarnFraction is WarnAt, or its default.
func (b Budget) WarnFraction() float64 {
	if b.WarnAt <= 0 || b.WarnAt >= 1 {
		return 0.8
	}
	return b.WarnAt
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
	if trusted && o.CheckpointRetentionDays != 0 {
		c.CheckpointRetentionDays = o.CheckpointRetentionDays
	}
	if o.Theme != "" {
		if _, err := ParseTheme(o.Theme); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		c.Theme = o.Theme
	}
	if o.Notifications != "" {
		if _, err := ParseNotifications(o.Notifications); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		c.Notifications = o.Notifications
	}
	if o.Language != "" {
		c.Language = o.Language
	}
	for _, b := range []struct{ dst, src **bool }{{&c.AutoCompact, &o.AutoCompact}, {&c.Verbose, &o.Verbose}, {&c.SpinnerTips, &o.SpinnerTips}} {
		if *b.src != nil {
			*b.dst = *b.src
		}
	}
	for k, v := range o.Roles {
		if c.Roles == nil {
			c.Roles = map[string]string{}
		}
		c.Roles[k] = strings.TrimSpace(v)
	}
	for k, v := range o.Fallbacks {
		if c.Fallbacks == nil {
			c.Fallbacks = map[string][]string{}
		}
		c.Fallbacks[k] = v
	}
	for k, v := range o.RoleOptions {
		if v.Isolation != "" && v.Isolation != "worktree" && v.Isolation != "none" {
			return fmt.Errorf("%s: role_options.%s.isolation must be \"worktree\" or \"none\"", path, k)
		}
		if v.MaxTurns < 0 {
			return fmt.Errorf("%s: role_options.%s.max_turns must not be negative", path, k)
		}
		if v.Context != "" && v.Context != "minimal" {
			return fmt.Errorf("%s: role_options.%s.context must be \"minimal\" or \"\"", path, k)
		}
		if c.RoleOptions == nil {
			c.RoleOptions = map[string]RoleOption{}
		}
		c.RoleOptions[k] = v
	}
	if o.Budget.SessionUSD < 0 || o.Budget.WarnAt < 0 {
		return fmt.Errorf("%s: budget values must not be negative", path)
	}
	if o.Budget.SessionUSD != 0 {
		c.Budget.SessionUSD = o.Budget.SessionUSD
	}
	if o.Budget.WarnAt != 0 {
		c.Budget.WarnAt = o.Budget.WarnAt
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

// Themes are the accepted values of the "theme" setting.
var Themes = []string{"auto", "dark", "light"}

// ParseTheme checks a theme name; "" means auto.
func ParseTheme(s string) (string, error) {
	if s == "" {
		return "auto", nil
	}
	for _, t := range Themes {
		if s == t {
			return s, nil
		}
	}
	return "", fmt.Errorf("unknown theme %q (want %s)", s, strings.Join(Themes, ", "))
}

// NotificationKinds are the accepted values of the "notifications" setting.
var NotificationKinds = []string{"off", "bell", "desktop"}

// ParseNotifications checks a notifications value; "" means off.
func ParseNotifications(s string) (string, error) {
	if s == "" {
		return "off", nil
	}
	for _, k := range NotificationKinds {
		if s == k {
			return s, nil
		}
	}
	return "", fmt.Errorf("unknown notifications %q (want %s)", s, strings.Join(NotificationKinds, ", "))
}

func on(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

// CheckpointRetention is how long /undo snapshots are kept; 0 means
// keep them forever.
func (c *Config) CheckpointRetention() time.Duration {
	switch d := c.CheckpointRetentionDays; {
	case d < 0:
		return 0
	case d == 0:
		d = 7
		fallthrough
	default:
		return time.Duration(d) * 24 * time.Hour
	}
}

// AutoCompactOn reports whether auto-compaction is enabled (default on).
func (c *Config) AutoCompactOn() bool { return on(c.AutoCompact, true) }

// VerboseOn reports whether verbose output is enabled (default off).
func (c *Config) VerboseOn() bool { return on(c.Verbose, false) }

// TipsOn reports whether spinner tips are shown (default on).
func (c *Config) TipsOn() bool { return on(c.SpinnerTips, true) }

// SetUserSetting writes a top-level setting to the user config, or removes
// it when value is nil or "", so the built-in default applies again. It
// does not update c; callers set the field they changed.
func (c *Config) SetUserSetting(key string, value any) error {
	return updateJSON(c.UserConfigPath(), 0o644, func(raw map[string]any) {
		if value == nil || value == "" {
			delete(raw, key)
			return
		}
		raw[key] = value
	})
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

// Routing is what the /routing wizard saves.
type Routing struct {
	Roles     map[string]string
	Fallbacks map[string][]string
	Options   map[string]RoleOption
	Budget    Budget
}

// SaveRouting writes roles, fallbacks and budget to the settings file at
// path, replacing those keys there (empty ones are removed), and applies
// them to c.
func (c *Config) SaveRouting(path string, r Routing) error {
	roles := map[string]any{}
	for k, v := range r.Roles {
		if v = strings.TrimSpace(v); v != "" {
			roles[k] = v
		}
	}
	// A role cleared here but set in another settings file is saved as ""
	// so it stays cleared (inherits) when the files are merged again.
	for k, v := range c.Routing().Roles {
		if _, kept := roles[k]; !kept && v != "" {
			roles[k] = ""
		}
	}
	fallbacks := map[string]any{}
	for k, v := range r.Fallbacks {
		if len(v) > 0 {
			fallbacks[k] = v
		}
	}
	options := map[string]any{}
	for k, v := range r.Options {
		if v != (RoleOption{}) {
			options[k] = v
		}
	}
	budget := map[string]any{}
	if r.Budget.SessionUSD > 0 {
		budget["session_usd"] = r.Budget.SessionUSD
	}
	if r.Budget.WarnAt > 0 {
		budget["warn_at"] = r.Budget.WarnAt
	}
	routingMu.Lock()
	defer routingMu.Unlock()
	err := updateJSON(path, 0o644, func(raw map[string]any) {
		for key, v := range map[string]map[string]any{"roles": roles, "fallbacks": fallbacks, "role_options": options, "budget": budget} {
			if len(v) == 0 {
				delete(raw, key)
			} else {
				raw[key] = v
			}
		}
	})
	if err != nil {
		return err
	}
	newRoles, newFallbacks, newOptions := map[string]string{}, map[string][]string{}, map[string]RoleOption{}
	for k, v := range options {
		newOptions[k] = v.(RoleOption)
	}
	for k, v := range roles {
		if v != "" {
			newRoles[k] = v.(string)
		}
	}
	for k, v := range fallbacks {
		newFallbacks[k] = slices.Clone(v.([]string))
	}
	c.Roles, c.Fallbacks, c.RoleOptions, c.Budget = newRoles, newFallbacks, newOptions, r.Budget
	return nil
}

// routingMu guards Roles, Fallbacks and Budget, which /routing changes
// while agents read them.
var routingMu sync.RWMutex

// Routing returns a copy of the roles, fallbacks and budget, safe to use
// while another goroutine saves new ones.
func (c *Config) Routing() Routing {
	routingMu.RLock()
	defer routingMu.RUnlock()
	r := Routing{Roles: make(map[string]string, len(c.Roles)), Fallbacks: make(map[string][]string, len(c.Fallbacks)), Options: maps.Clone(c.RoleOptions), Budget: c.Budget}
	if r.Options == nil {
		r.Options = map[string]RoleOption{}
	}
	for k, v := range c.Roles {
		r.Roles[k] = v
	}
	for k, v := range c.Fallbacks {
		r.Fallbacks[k] = slices.Clone(v)
	}
	return r
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
