package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"larik/internal/config"
)

// TestHiddenSkillsSetting: the row edits your own patterns for this
// project, in your private project settings, and never copies a shared
// file's patterns into them or into the user config.
func TestHiddenSkillsSetting(t *testing.T) {
	m := testModel(t)
	cfg := m.opts.Config
	local := config.LocalSettingsPath(cfg.Cwd)
	t.Cleanup(func() { os.Remove(local) })
	shared := filepath.Join(cfg.Cwd, ".larik", "settings.json")
	os.MkdirAll(filepath.Dir(shared), 0o755)
	os.WriteFile(shared, []byte(`{"skills":{"hide":["shared-*"]}}`), 0o644)
	cfg.Skills.Hide = []string{"shared-*"} // as Load gave it

	if got := settingValue(t, m, "skills_hide"); got != "" {
		t.Fatalf("the row should not show the shared file's patterns as yours: %q", got)
	}
	if _, _, err := m.saveSetting("skills_hide", "cmux-*, [bad"); err == nil || !strings.Contains(err.Error(), "[bad") {
		t.Fatalf("a malformed pattern would fail the next start: %v", err)
	}
	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Fatal("a rejected value must not reach a settings file")
	}

	_, msg, err := m.saveSetting("skills_hide", " cmux-*, ,pdf ")
	if err != nil || !strings.Contains(msg, shortHome(local)) {
		t.Fatalf("save: %q %v", msg, err)
	}
	if got := strings.Join(config.ListAt(local, "skills.hide"), ","); got != "cmux-*,pdf" {
		t.Errorf("private project settings hold %q", got)
	}
	if _, err := os.Stat(cfg.UserConfigPath()); !os.IsNotExist(err) {
		t.Errorf("the user config, which every project reads, must stay untouched:\n%s", savedConfig(t, m))
	}
	if got := strings.Join(cfg.Skills.Hide, ","); got != "shared-*,cmux-*,pdf" {
		t.Errorf("in force: %q, want the shared patterns plus yours", got)
	}
	if got := settingValue(t, m, "skills_hide"); got != "cmux-*, pdf" {
		t.Errorf("row shows %q", got)
	}

	if _, _, err := m.saveSetting("skills_hide", ""); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.Skills.Hide, ","); got != "shared-*" || len(config.ListAt(local, "skills.hide")) != 0 {
		t.Errorf("empty clears only yours: in force %q", got)
	}
}
