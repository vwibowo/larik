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
	write(t, cfg0.UserConfigPath(), `{"theme":"dark","roles":{"worker":"groq/llama-4-scout","explore":"gemini/gemini-3.8-flash"},"fallbacks":{"worker":["ollama/qwen3-coder"]},"budget":{"session_usd":2,"warn_at":0.5}}`)
	write(t, LocalSettingsPath(cwd), `{"roles":{"explore":"ollama/qwen3:4b"}}`)

	cfg, err := Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.Routing()
	if r.Roles["worker"] != "groq/llama-4-scout" || r.Roles["explore"] != "ollama/qwen3:4b" || r.Fallbacks["worker"][0] != "ollama/qwen3-coder" {
		t.Errorf("loaded routing = %+v", r)
	}
	if r.Budget.SessionUSD != 2 || r.Budget.WarnFraction() != 0.5 {
		t.Errorf("budget = %+v", r.Budget)
	}

	// Clear worker in the project file: it must stay cleared after a
	// reload even though the user file still sets it.
	r.Roles = map[string]string{"explore": "ollama/qwen3:4b", "smart": "anthropic/claude-opus-5"}
	r.Fallbacks = nil
	r.Budget = Budget{SessionUSD: 1.5}
	if err := cfg.SaveRouting(LocalSettingsPath(cwd), r); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Routing(); got.Roles["worker"] != "" || got.Roles["smart"] == "" || got.Budget.SessionUSD != 1.5 {
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
	write(t, LocalSettingsPath(cwd), `{"role_options":{"worker":{"isolation":"worktree","max_turns":40}}}`)
	cfg, err := Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if o := cfg.Routing().Options["worker"]; o.Isolation != "worktree" || o.MaxTurns != 40 {
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

	for _, bad := range []string{`{"role_options":{"w":{"isolation":"docker"}}}`, `{"role_options":{"w":{"max_turns":-1}}}`} {
		write(t, LocalSettingsPath(cwd), bad)
		if _, err := Load(cwd); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
