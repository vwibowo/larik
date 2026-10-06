package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoutingLoadsAndSaves(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg0 := &Config{ConfigDir: filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "larik")}
	write(t, cfg0.UserConfigPath(), `{"theme":"dark","delegation":"balanced","roles":{"worker":"groq/llama-4-scout","explore":"gemini/gemini-3.8-flash"},"fallbacks":{"worker":["ollama/qwen3-coder"]},"budget":{"session_usd":2,"warn_at":0.5}}`)
	write(t, LocalSettingsPath(cwd), `{"roles":{"explore":"ollama/qwen3:4b"}}`)

	cfg, err := Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.Routing()
	if r.Roles["worker"] != "groq/llama-4-scout" || r.Roles["explore"] != "ollama/qwen3:4b" || r.Fallbacks["worker"][0] != "ollama/qwen3-coder" {
		t.Errorf("loaded routing = %+v", r)
	}
	if r.Delegation != DelegationBalanced {
		t.Errorf("delegation = %q", r.Delegation)
	}
	if r.Budget.SessionUSD != 2 || r.Budget.WarnFraction() != 0.5 {
		t.Errorf("budget = %+v", r.Budget)
	}

	// Clear worker in the project file: it must stay cleared after a
	// reload even though the user file still sets it.
	r.Roles = map[string]string{"explore": "ollama/qwen3:4b", "smart": "anthropic/claude-opus-5"}
	r.Fallbacks = nil
	r.Delegation = DelegationAggressive
	r.Budget = Budget{SessionUSD: 1.5}
	if err := cfg.SaveRouting(LocalSettingsPath(cwd), r); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Routing(); got.Roles["worker"] != "" || got.Roles["smart"] == "" || got.Delegation != DelegationAggressive || got.Budget.SessionUSD != 1.5 {
		t.Errorf("after save = %+v", got)
	}
	var raw map[string]any
	data, _ := os.ReadFile(LocalSettingsPath(cwd))
	json.Unmarshal(data, &raw)
	if roles := raw["roles"].(map[string]any); roles["worker"] != "" {
		t.Errorf("a cleared role should be saved as \"\": %s", data)
	}
	if _, ok := raw["fallbacks"]; ok {
		t.Errorf("empty fallbacks should be removed: %s", data)
	}

	cfg, err = Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Routing(); got.Roles["worker"] != "" || got.Roles["smart"] != "anthropic/claude-opus-5" || got.Budget.SessionUSD != 1.5 {
		t.Errorf("reloaded = %+v", got)
	}
	// Other keys in the user file survive.
	data, _ = os.ReadFile(cfg.UserConfigPath())
	if !strings.Contains(string(data), `"theme": "dark"`) && !strings.Contains(string(data), `"theme":"dark"`) {
		t.Errorf("user config lost other keys: %s", data)
	}
}

func TestDelegationPolicyValidationAndSharedTightening(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	user := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "larik", "config.json")
	write(t, user, `{"delegation":"aggressive"}`)
	write(t, filepath.Join(cwd, ".larik", "settings.json"), `{"delegation":"balanced"}`)
	cfg, err := Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Routing().Delegation != DelegationBalanced {
		t.Fatalf("shared config did not tighten delegation: %q", cfg.Routing().Delegation)
	}

	write(t, filepath.Join(cwd, ".larik", "settings.json"), `{"delegation":"aggressive"}`)
	write(t, user, `{"delegation":"manual"}`)
	cfg, err = Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Routing().Delegation != DelegationManual {
		t.Fatalf("shared config widened delegation: %q", cfg.Routing().Delegation)
	}

	write(t, user, `{"delegation":"eager"}`)
	if _, err := Load(cwd); err == nil || !strings.Contains(err.Error(), "delegation must be") {
		t.Fatalf("invalid personal policy: %v", err)
	}
}

func TestNegativeBudgetRejected(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	write(t, LocalSettingsPath(cwd), `{"budget":{"session_usd":-1}}`)
	if _, err := Load(cwd); err == nil {
		t.Errorf("a negative budget should fail to load")
	}
}

func TestRoleOptions(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	write(t, LocalSettingsPath(cwd), `{"role_options":{"worker":{"isolation":"worktree","max_turns":40,"context":"minimal"}}}`)
	cfg, err := Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if o := cfg.Routing().Options["worker"]; o.Isolation != "worktree" || o.MaxTurns != 40 || o.Context != "minimal" {
		t.Fatalf("options = %+v", o)
	}
	r := cfg.Routing()
	r.Options["explore"] = RoleOption{MaxTurns: 30}
	r.Options["worker"] = RoleOption{}
	if err := cfg.SaveRouting(LocalSettingsPath(cwd), r); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(LocalSettingsPath(cwd))
	if !strings.Contains(string(data), `"max_turns": 30`) || strings.Contains(string(data), `"worker"`) {
		t.Errorf("saved %s", data)
	}

	for _, bad := range []string{`{"role_options":{"w":{"isolation":"docker"}}}`, `{"role_options":{"w":{"max_turns":-1}}}`, `{"role_options":{"w":{"context":"full"}}}`} {
		write(t, LocalSettingsPath(cwd), bad)
		if _, err := Load(cwd); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestTokenBudgetPersistsAndSharedFilesCanOnlyLowerIt(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.Routing()
	r.Budget = Budget{SessionTokens: 500000}
	if err := cfg.SaveRouting(cfg.UserConfigPath(), r); err != nil {
		t.Fatal(err)
	}
	cfg, _ = Load(cwd)
	if got := cfg.Routing().Budget.SessionTokens; got != 500000 {
		t.Fatalf("saved token budget = %d", got)
	}

	write(t, filepath.Join(cwd, ".larik", "settings.json"), `{"budget":{"session_tokens":900000}}`)
	if cfg, _ = Load(cwd); cfg.Budget.SessionTokens != 500000 {
		t.Errorf("a shared file raised the cap to %d", cfg.Budget.SessionTokens)
	}
	write(t, filepath.Join(cwd, ".larik", "settings.json"), `{"budget":{"session_tokens":100000}}`)
	if cfg, _ = Load(cwd); cfg.Budget.SessionTokens != 100000 {
		t.Errorf("a shared file may lower the cap, got %d", cfg.Budget.SessionTokens)
	}
	write(t, filepath.Join(cwd, ".larik", "settings.json"), `{"budget":{"session_tokens":-1}}`)
	if _, err := Load(cwd); err == nil {
		t.Error("a negative token budget should fail to load")
	}
}
