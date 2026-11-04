package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobalSkillsInstallAndConflict(t *testing.T) {
	home := t.TempDir()
	if err := InstallSkills(home); err != nil {
		t.Fatal(err)
	}
	if !SkillsInstalled(home) {
		t.Fatal("skills not installed in shared and Claude locations")
	}
	if err := InstallSkills(home); err != nil {
		t.Fatalf("repeat install: %v", err)
	}
	if err := RemoveSkills(home); err != nil || SkillsInstalled(home) {
		t.Fatalf("remove skills: %v", err)
	}
	conflict := filepath.Join(skillDir(home, "larik-desktop"), "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(conflict), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conflict, []byte("someone else's skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InstallSkills(home); err == nil || !strings.Contains(err.Error(), "another skill") {
		t.Fatalf("did not detect conflict: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skillDir(home, "larik-desktop-browser"), "SKILL.md")); !os.IsNotExist(err) {
		t.Fatal("preflight failure partially installed the other skill")
	}
	if err := os.Remove(conflict); err != nil {
		t.Fatal(err)
	}
	link := claudeLink(home, "larik-desktop")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte("another skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InstallSkills(home); err == nil {
		t.Fatal("Claude skill name conflict was overwritten")
	}
}

func TestHookInstallPreservesSettingsAndUninstalls(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	for _, agent := range Agents() {
		t.Run(agent, func(t *testing.T) {
			home := t.TempDir()
			path := HookPath(home, agent)
			if agent != "opencode" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				original := `{"theme":"dark","hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo other"}]}]}}`
				if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := InstallHook(home, "/usr/local/bin/larik", agent); err != nil {
				t.Fatal(err)
			}
			installed, err := HookInstalled(home, agent)
			if err != nil || !installed {
				t.Fatalf("installed: %v %v", installed, err)
			}
			first, _ := os.ReadFile(path)
			if err := InstallHook(home, "/usr/local/bin/larik", agent); err != nil {
				t.Fatal(err)
			}
			second, _ := os.ReadFile(path)
			if string(first) != string(second) {
				t.Fatal("repeat install changed the configuration")
			}
			if err := RemoveHook(home, agent); err != nil {
				t.Fatal(err)
			}
			installed, err = HookInstalled(home, agent)
			if err != nil || installed {
				t.Fatalf("removed: %v %v", installed, err)
			}
			if agent != "opencode" {
				data, _ := os.ReadFile(path)
				var cfg map[string]json.RawMessage
				if json.Unmarshal(data, &cfg) != nil || string(cfg["theme"]) != `"dark"` || !strings.Contains(string(data), "echo other") {
					t.Fatalf("unrelated settings lost: %s", data)
				}
				matches, _ := filepath.Glob(path + ".bak.*")
				if len(matches) == 0 {
					t.Fatal("settings were changed without a backup")
				}
			}
		})
	}
}

func TestHookInstallRejectsMalformedAndUnowned(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	for _, agent := range []string{"claude", "codex", "gemini"} {
		t.Run(agent, func(t *testing.T) {
			home := t.TempDir()
			path := HookPath(home, agent)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			original := []byte(`{"hooks": broken}`)
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := InstallHook(home, "/usr/local/bin/larik", agent); err == nil {
				t.Fatal("malformed settings were overwritten")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(original) {
				t.Fatalf("malformed settings changed: %s %v", got, err)
			}
		})
	}
	home := t.TempDir()
	plugin := HookPath(home, "opencode")
	if err := os.MkdirAll(filepath.Dir(plugin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plugin, []byte("export const Other = () => ({})"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InstallHook(home, "/usr/local/bin/larik", "opencode"); err == nil {
		t.Fatal("unowned plugin was overwritten")
	}
	linkedHome := t.TempDir()
	linkedPath := HookPath(linkedHome, "claude")
	if err := os.MkdirAll(filepath.Dir(linkedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(outside, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, linkedPath); err != nil {
		t.Fatal(err)
	}
	if err := InstallHook(linkedHome, "/usr/local/bin/larik", "claude"); err == nil {
		t.Fatal("linked settings file was replaced")
	}
}
