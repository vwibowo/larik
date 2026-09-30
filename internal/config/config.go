// Package config loads layered JSON settings:
//
//	~/.config/larik/config.json      user defaults
//	./.larik/settings.json           project settings (commit this)
//	~/.config/larik/projects/<id>.json  personal project settings and approvals
//
// Later trusted files override scalars. Shared files may only tighten
// permissions; MCP servers are also read from .mcp.json (see mcp.go).
package config

import (
	"bytes"
	"crypto/sha256"
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

	"larik/internal/filelock"
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
	// Debug records every session's requests, responses and tool calls
	// for `larik trace`. Honored only from personal files: traces hold
	// prompts and file contents.
	Debug *bool `json:"debug,omitempty"`
	// DebugRetentionDays is how long traces are kept: 0 means the
	// default (14), a negative value keeps them forever.
	DebugRetentionDays int `json:"debug_retention_days,omitempty"`
	// Theme picks the TUI colors: "auto" (follow the terminal's
	// background, the default), "dark" or "light".
	Theme string `json:"theme,omitempty"`
	// AutoCompact summarizes the conversation when the context is nearly
	// full. Nil means on.
	AutoCompact *bool `json:"auto_compact,omitempty"`
	// TokenSaver filters recognized bash output before it enters model context.
	TokenSaver *bool `json:"token_saver,omitempty"`
	// Verbose shows tool output and thinking in full in the TUI.
	Verbose *bool `json:"verbose,omitempty"`
	// Notifications alerts an unfocused terminal when larik needs you:
	// "off" (the default), "bell" or "desktop".
	Notifications string `json:"notifications,omitempty"`
	// Language is what the model replies in; empty leaves it to the model.
	Language string `json:"language,omitempty"`
	// SpinnerTips shows a tip under the spinner during a turn. Nil means on.
	SpinnerTips *bool `json:"spinner_tips,omitempty"`
	// Mouse lets the TUI take the mouse for wheel scrolling. Off leaves the
	// mouse to the terminal, so text can be selected without a modifier.
	// Nil means on.
	Mouse *bool `json:"mouse,omitempty"`
	// StatusLine runs a command whose output becomes the TUI footer.
	// Honored only from personal files, since it runs a command.
	StatusLine *StatusLine `json:"status_line,omitempty"`
	// Keybindings rebind TUI actions: action name to one key or a list;
	// an empty list unbinds. Honored only from personal files.
	Keybindings map[string]KeyList `json:"keybindings,omitempty"`
	// EditorMode is how the prompt is edited: "normal" (the default) or
	// "vim". Honored only from personal files.
	EditorMode string `json:"editor_mode,omitempty"`

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
	// the user approved. Only honored from private project settings.
	ApprovedMCP map[string]string `json:"approved_mcp_servers,omitempty"`
	Hooks       hooks.Config      `json:"hooks,omitempty"`
	// ApprovedHooks is the hash of the approved project hook set (from
	// .larik/settings.json). Only honored from private project settings.
	ApprovedHooks string `json:"approved_project_hooks,omitempty"`

	// Web configures web_fetch and web_search. The search backend (which
	// receives every query) is honored only from personal files.
	Web WebConfig `json:"web,omitempty"`

	// ToolSearch decides whether MCP tools are loaded on demand through
	// tool_search instead of being sent with every request: "auto" (the
	// default: when there are many), "on" or "off".
	ToolSearch string `json:"tool_search,omitempty"`

	// Memory configures the notes Larik keeps across sessions. Shared
	// files may only switch it off.
	Memory MemoryConfig `json:"memory,omitempty"`

	// AutoMode configures auto mode's safety check. Only personal files
	// may set it: a shared file choosing the model that approves its own
	// repository's commands would defeat the check.
	AutoMode AutoModeConfig `json:"auto_mode,omitempty"`

	// Browser configures the browser_* tools. It launches a program, so
	// only personal files may enable it; shared files may only disable it.
	Browser BrowserConfig `json:"browser,omitempty"`

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

// StatusLine is a command whose output replaces the footer's model,
// context and cost. It gets the session's state as JSON on stdin, in
// Claude Code's statusLine form, so the same scripts work.
type StatusLine struct {
	// Type is "command", the only kind, and may be left out.
	Type    string `json:"type,omitempty"`
	Command string `json:"command"`
}

// KeyList is one key ("ctrl+e") or several.
type KeyList []string

func (k *KeyList) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*k = KeyList{one}
		if one == "" {
			*k = KeyList{}
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return errors.New("a key binding is a key or a list of keys")
	}
	*k = append(KeyList{}, many...)
	return nil
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

// LocalSettingsPath is outside the repository, so project files cannot
// impersonate a trusted personal settings layer.
func LocalSettingsPath(cwd string) string {
	canonical := filepath.Clean(cwd)
	if resolved, err := filepath.EvalSymlinks(canonical); err == nil {
		canonical = resolved
	}
	hash := sha256.Sum256([]byte(canonical))
	return filepath.Join(configDir(), "projects", fmt.Sprintf("%s-%x.json", filepath.Base(canonical), hash[:8]))
}

func Load(cwd string) (*Config, error) {
	cfg := &Config{
		MaxTurns:    200,
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
	// An empty file is no settings: the Linux sandbox leaves empty
	// placeholders (.mcp.json) in the project while a command runs.
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	var o struct {
		Config
		MCPServersCompat map[string]MCPServer `json:"mcpServers"` // .mcp.json / Claude Code format
		StatusLineCompat *StatusLine          `json:"statusLine"` // Claude Code format
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
	if trusted && o.ToolSearch != "" {
		c.ToolSearch = o.ToolSearch
	}
	if o.Memory.Enabled != nil && (trusted || !*o.Memory.Enabled) {
		c.Memory.Enabled = o.Memory.Enabled
	}
	if trusted && o.AutoMode.Model != "" {
		c.AutoMode.Model = o.AutoMode.Model
	}
	if b := browserSection(data); b != nil {
		if trusted {
			if b.Enabled != nil {
				c.Browser.Enabled = *b.Enabled
			}
			if b.Headless != nil {
				c.Browser.Headless = *b.Headless
			}
			if b.ChromePath != nil {
				c.Browser.ChromePath = *b.ChromePath
			}
		} else if b.Enabled != nil && !*b.Enabled {
			c.Browser.Enabled = false // a shared file may only switch it off
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
		c.Sandbox.AllowedDomains = append(c.Sandbox.AllowedDomains, o.Sandbox.AllowedDomains...)
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
			prev.Disabled = prev.Disabled || srv.Disabled
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
	if trusted && path == LocalSettingsPath(c.Cwd) {
		for k, v := range o.ApprovedMCP {
			c.ApprovedMCP[k] = v
		}
		if o.ApprovedHooks != "" {
			c.ApprovedHooks = o.ApprovedHooks
		}
	}
	if trusted && o.Model != "" {
		c.Model = o.Model
	}
	if trusted && o.Effort != "" {
		c.Effort = o.Effort
	}
	if trusted && o.Mode != "" {
		c.Mode = o.Mode
	}
	if o.MaxTurns != 0 && (trusted || c.MaxTurns == 0 || o.MaxTurns < c.MaxTurns) {
		c.MaxTurns = o.MaxTurns
	}
	if trusted && o.CheckpointRetentionDays != 0 {
		c.CheckpointRetentionDays = o.CheckpointRetentionDays
	}
	if trusted && o.Debug != nil {
		c.Debug = o.Debug
	}
	if trusted && o.DebugRetentionDays != 0 {
		c.DebugRetentionDays = o.DebugRetentionDays
	}
	// A bad value in your own file is an error to fix; one in a file that
	// came with the repository is ignored, so a clone can't stop Larik
	// from starting.
	if o.Theme != "" {
		if _, err := ParseTheme(o.Theme); err == nil {
			c.Theme = o.Theme
		} else if trusted {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	if o.Notifications != "" {
		if _, err := ParseNotifications(o.Notifications); err == nil {
			c.Notifications = o.Notifications
		} else if trusted {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	if o.Language != "" {
		c.Language = o.Language
	}
	for _, b := range []struct{ dst, src **bool }{{&c.AutoCompact, &o.AutoCompact}, {&c.Verbose, &o.Verbose}, {&c.SpinnerTips, &o.SpinnerTips}, {&c.Mouse, &o.Mouse}} {
		if *b.src != nil {
			*b.dst = *b.src
		}
	}
	for _, sl := range []*StatusLine{o.StatusLineCompat, o.StatusLine} {
		if sl == nil || !trusted {
			continue // a shared file can't make Larik run a command
		}
		if sl.Type != "" && sl.Type != "command" {
			return fmt.Errorf("%s: status_line.type must be \"command\"", path)
		}
		c.StatusLine = sl
		if strings.TrimSpace(sl.Command) == "" {
			c.StatusLine = nil // a later file can switch it off
		}
	}
	if trusted && o.EditorMode != "" {
		if o.EditorMode != "normal" && o.EditorMode != "vim" {
			return fmt.Errorf("%s: editor_mode must be \"normal\" or \"vim\"", path)
		}
		c.EditorMode = o.EditorMode
	}
	for action, keys := range o.Keybindings {
		if !trusted {
			continue // nor rebind your keys
		}
		if c.Keybindings == nil {
			c.Keybindings = map[string]KeyList{}
		}
		c.Keybindings[action] = keys
	}
	if trusted && o.TokenSaver != nil {
		c.TokenSaver = o.TokenSaver
	}
	for k, v := range o.Roles {
		if !trusted {
			continue
		}
		if c.Roles == nil {
			c.Roles = map[string]string{}
		}
		c.Roles[k] = strings.TrimSpace(v)
	}
	for k, v := range o.Fallbacks {
		if !trusted {
			continue
		}
		if c.Fallbacks == nil {
			c.Fallbacks = map[string][]string{}
		}
		c.Fallbacks[k] = v
	}
	for k, v := range o.RoleOptions {
		if !trusted {
			continue
		}
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
	if o.Budget.SessionUSD != 0 && (trusted || c.Budget.SessionUSD == 0 || o.Budget.SessionUSD < c.Budget.SessionUSD) {
		c.Budget.SessionUSD = o.Budget.SessionUSD
	}
	if trusted && o.Budget.WarnAt != 0 {
		c.Budget.WarnAt = o.Budget.WarnAt
	}
	if trusted {
		for k, v := range o.Providers {
			c.Providers[k] = v
		}
		for k, v := range o.Models {
			c.Models[k] = v
		}
		c.Permissions.Allow = append(c.Permissions.Allow, o.Permissions.Allow...)
	}
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

// updateLocal edits private project settings as generic JSON so unknown keys survive.
func updateLocal(cwd string, edit func(raw map[string]any)) error {
	return updateJSON(LocalSettingsPath(cwd), 0o600, edit)
}

// updateJSON edits a settings file as generic JSON so unknown keys survive.
// A file that exists keeps its permissions, unless perm is private (no
// group or other access), which is then enforced. The sidecar lock covers
// the entire read-modify-write sequence across processes.
func updateJSON(path string, perm os.FileMode, edit func(raw map[string]any)) error {
	_, err := updateJSONIf(path, perm, true, func(raw map[string]any) bool {
		edit(raw)
		return true
	})
	return err
}

// updateJSONIf runs edit under the settings lock. It leaves the file alone
// when create is false and the file is absent, or edit reports no change.
func updateJSONIf(path string, perm os.FileMode, create bool, edit func(raw map[string]any) bool) (bool, error) {
	if !create {
		if _, err := os.Stat(filepath.Dir(path)); os.IsNotExist(err) {
			return false, nil
		} else if err != nil {
			return false, err
		}
	}
	dirPerm := os.FileMode(0o755)
	if perm&0o077 == 0 {
		dirPerm = 0o700
	}
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return false, err
	}
	if dirPerm == 0o700 {
		if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
			return false, err
		}
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, fmt.Errorf("open settings lock: %w", err)
	}
	defer lock.Close()
	if err := lock.Chmod(0o600); err != nil {
		return false, fmt.Errorf("secure settings lock: %w", err)
	}
	if err := filelock.Lock(lock, false); err != nil {
		return false, fmt.Errorf("lock settings: %w", err)
	}
	defer filelock.Unlock(lock)
	raw := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &raw); err != nil {
			return false, fmt.Errorf("%s: %w", path, err)
		}
	} else if os.IsNotExist(err) {
		if !create {
			return false, nil
		}
	} else {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	if !edit(raw) {
		return false, nil
	}
	if fi, err := os.Stat(path); err == nil {
		private := perm&0o077 == 0
		perm = fi.Mode().Perm()
		if private {
			perm &^= 0o077
		}
	}
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".larik-config-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return false, err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return false, err
	}
	return true, nil
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
// DebugOn reports whether sessions are traced.
func (c *Config) DebugOn() bool { return on(c.Debug, false) }

// DebugRetention is how long traces are kept; zero means forever.
func (c *Config) DebugRetention() time.Duration {
	switch d := c.DebugRetentionDays; {
	case d < 0:
		return 0
	case d == 0:
		return 14 * 24 * time.Hour
	default:
		return time.Duration(d) * 24 * time.Hour
	}
}

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

// TokenSaverOn reports whether command output filtering is enabled (default off).
func (c *Config) TokenSaverOn() bool { return on(c.TokenSaver, false) }

// VerboseOn reports whether verbose output is enabled (default off).
func (c *Config) VerboseOn() bool { return on(c.Verbose, false) }

// TipsOn reports whether spinner tips are shown (default on).
func (c *Config) TipsOn() bool { return on(c.SpinnerTips, true) }

func (c *Config) MouseOn() bool { return on(c.Mouse, true) }

// SetUserSetting writes a top-level setting to the user config, or removes
// it when value is nil or "", so the built-in default applies again. It
// does not update c; callers set the field they changed.
func (c *Config) SetUserSetting(key string, value any) error {
	return c.SetUserSettings(map[string]any{key: value})
}

// SetUserSettings saves related defaults atomically, preserving unrelated
// settings and concurrent updates to the config file.
func (c *Config) SetUserSettings(values map[string]any) error {
	return updateJSON(c.UserConfigPath(), 0o644, func(raw map[string]any) {
		for key, value := range values {
			if value == nil || value == "" {
				delete(raw, key)
			} else {
				raw[key] = value
			}
		}
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
	if pc.APIKey != "" || path == LocalSettingsPath(c.Cwd) {
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
	perm := os.FileMode(0o644)
	if path == LocalSettingsPath(c.Cwd) {
		perm = 0o600
	}
	err := updateJSON(path, perm, func(raw map[string]any) {
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
// user config and private project settings), along with a default model
// in the same file that uses it. It returns the files it changed. Shared
// project settings are left alone; their provider definitions are ignored.
func (c *Config) RemoveProvider(name string) ([]string, error) {
	var changed []string
	for _, path := range []string{c.UserConfigPath(), LocalSettingsPath(c.Cwd)} {
		changedFile, err := updateJSONIf(path, 0o644, false, func(raw map[string]any) bool {
			ps, _ := raw["providers"].(map[string]any)
			_, has := ps[name]
			model, _ := raw["model"].(string)
			usesIt := strings.HasPrefix(model, name+"/")
			if !has && !usesIt {
				return false
			}
			if ps, _ := raw["providers"].(map[string]any); ps != nil {
				delete(ps, name)
				if len(ps) == 0 {
					delete(raw, "providers")
				}
			}
			if usesIt {
				delete(raw, "model")
			}
			return true
		})
		if err != nil {
			return changed, err
		}
		if changedFile {
			changed = append(changed, path)
		}
	}
	delete(c.Providers, name)
	if strings.HasPrefix(c.Model, name+"/") {
		c.Model = ""
	}
	return changed, nil
}

// ProviderInShared reports whether shared project settings contain a provider
// definition. Such definitions are ignored when loading the active config.
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

// MemoryConfig is the "memory" settings section.
type MemoryConfig struct {
	// Enabled defaults to true.
	Enabled *bool `json:"enabled,omitempty"`
}

// MemoryOn reports whether memory is enabled (default on).
func (c *Config) MemoryOn() bool { return on(c.Memory.Enabled, true) }

// AutoModeConfig is the "auto_mode" settings section.
type AutoModeConfig struct {
	// Model is the provider/model or routing role that judges calls in
	// auto mode; empty uses the session's model.
	Model string `json:"model,omitempty"`
}

// BrowserConfig is the "browser" settings section: the browser_* tools,
// which drive a Chrome window. Off unless a personal file enables it.
type BrowserConfig struct {
	Enabled  bool `json:"enabled,omitempty"`
	Headless bool `json:"headless,omitempty"`
	// ChromePath overrides the Chrome or Chromium binary to launch.
	ChromePath string `json:"chrome_path,omitempty"`
}

// browserPatch is one file's "browser" section with presence kept, so a
// later file can switch a setting back off.
type browserPatch struct {
	Enabled    *bool   `json:"enabled"`
	Headless   *bool   `json:"headless"`
	ChromePath *string `json:"chrome_path"`
}

func browserSection(data []byte) *browserPatch {
	var o struct {
		Browser *browserPatch `json:"browser"`
	}
	_ = json.Unmarshal(data, &o) // the whole file already parsed once
	return o.Browser
}

// WebConfig is the "web" settings section.
type WebConfig struct {
	FetchDisabled bool             `json:"fetch_disabled,omitempty"`
	Search        web.SearchConfig `json:"search,omitempty"`
}
