package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// userConfig prepares a personal config file holding seed and returns its path.
func userConfig(t *testing.T, seed string) (*Config, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(configDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir(), "config.json")
	if seed != "" {
		if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return cfg, path
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw := map[string]any{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return raw
}

// A setting inside a section is written without disturbing the rest of it, or
// anything else in the file — including keys this version of Larik does not
// know about, which may belong to a newer one.
func TestSetUserSettingPathKeepsEverythingElse(t *testing.T) {
	cfg, path := userConfig(t, `{"model":"anthropic/claude-opus-5","audio":{"enabled":true,"stt":{"model":"asr"}},"future_key":{"a":1}}`)
	if err := cfg.SetUserSettingPath("audio.stt.language", "id"); err != nil {
		t.Fatal(err)
	}
	raw := readJSON(t, path)
	if raw["model"] != "anthropic/claude-opus-5" || raw["future_key"] == nil {
		t.Errorf("unrelated settings were lost: %v", raw)
	}
	audio, _ := raw["audio"].(map[string]any)
	stt, _ := audio["stt"].(map[string]any)
	if audio["enabled"] != true || stt["model"] != "asr" || stt["language"] != "id" {
		t.Errorf("audio section = %v", audio)
	}
}

// Writing into a section that isn't there yet creates it.
func TestSetUserSettingPathCreatesTheSection(t *testing.T) {
	cfg, path := userConfig(t, `{"theme":"dark"}`)
	if err := cfg.SetUserSettingPath("web.search.provider", "brave"); err != nil {
		t.Fatal(err)
	}
	web, _ := readJSON(t, path)["web"].(map[string]any)
	search, _ := web["search"].(map[string]any)
	if search["provider"] != "brave" {
		t.Fatalf("web section = %v", web)
	}
}

// Clearing a setting removes it, and any section it empties, so the file says
// only what you actually chose and the built-in default applies again.
func TestSetUserSettingPathPrunesEmptySections(t *testing.T) {
	cfg, path := userConfig(t, `{"theme":"dark","audio":{"stt":{"language":"id"}}}`)
	if err := cfg.SetUserSettingPath("audio.stt.language", ""); err != nil {
		t.Fatal(err)
	}
	raw := readJSON(t, path)
	if _, still := raw["audio"]; still {
		t.Errorf("the emptied audio section should be gone: %v", raw)
	}
	if raw["theme"] != "dark" {
		t.Errorf("an unrelated setting was lost: %v", raw)
	}
}

// A section that keeps other settings is not removed with the one cleared.
func TestSetUserSettingPathKeepsSectionsStillInUse(t *testing.T) {
	cfg, path := userConfig(t, `{"audio":{"enabled":true,"stt":{"language":"id","model":"asr"}}}`)
	if err := cfg.SetUserSettingPath("audio.stt.language", ""); err != nil {
		t.Fatal(err)
	}
	audio, _ := readJSON(t, path)["audio"].(map[string]any)
	stt, _ := audio["stt"].(map[string]any)
	if audio["enabled"] != true || stt["model"] != "asr" {
		t.Errorf("audio section = %v", audio)
	}
	if _, still := stt["language"]; still {
		t.Errorf("language should be gone: %v", stt)
	}
}

// false is a value, not an absence: a switch turned off has to be written,
// or it would read as "never set" and fall back to a default of on.
func TestSetUserSettingPathWritesFalse(t *testing.T) {
	cfg, path := userConfig(t, "")
	if err := cfg.SetUserSettingPath("browser.enabled", false); err != nil {
		t.Fatal(err)
	}
	browser, _ := readJSON(t, path)["browser"].(map[string]any)
	if v, ok := browser["enabled"]; !ok || v != false {
		t.Fatalf("browser section = %v", browser)
	}
}

// Anything that may carry a credential is kept readable only by its owner.
func TestSecretSectionsAreWrittenPrivately(t *testing.T) {
	cfg, path := userConfig(t, "")
	if err := cfg.SetUserSettingPath("theme", "dark"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetUserSettingPath("audio.stt.api_key", "sk-secret"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("a file holding an API key is readable by others: %v", fi.Mode().Perm())
	}
}

func TestListItemsRoundTrip(t *testing.T) {
	cfg, path := userConfig(t, `{"sandbox":{"network":true}}`)
	file := cfg.UserConfigPath()
	for _, dir := range []string{"/tmp/one", "/tmp/two"} {
		if err := AddListItem(file, "sandbox.writable", dir); err != nil {
			t.Fatal(err)
		}
	}
	if err := AddListItem(file, "sandbox.writable", "/tmp/one"); err != nil {
		t.Fatal(err) // already there: must not be added twice
	}
	sandbox, _ := readJSON(t, path)["sandbox"].(map[string]any)
	list, _ := sandbox["writable"].([]any)
	if len(list) != 2 || list[0] != "/tmp/one" || list[1] != "/tmp/two" {
		t.Fatalf("writable = %v", sandbox["writable"])
	}
	if sandbox["network"] != true {
		t.Errorf("an unrelated setting in the section was lost: %v", sandbox)
	}
	for _, dir := range []string{"/tmp/one", "/tmp/two"} {
		if err := RemoveListItem(file, "sandbox.writable", dir); err != nil {
			t.Fatal(err)
		}
	}
	sandbox, _ = readJSON(t, path)["sandbox"].(map[string]any)
	if _, still := sandbox["writable"]; still {
		t.Errorf("an emptied list should be removed: %v", sandbox)
	}
	if sandbox["network"] != true {
		t.Errorf("removing the list should not empty the section: %v", sandbox)
	}
}

// A settings file may have been hand-edited into a shape Larik does not
// expect — here a lone string where a list belongs, which Load itself
// refuses. Editing it must not panic; it writes the list the setting needs.
func TestListItemsSurviveAWrongType(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(file, []byte(`{"permissions":{"allow":"bash(ls)"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AddListItem(file, "permissions.allow", "bash(go test*)"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveListItem(file, "permissions.deny", "nothing"); err != nil {
		t.Fatal(err)
	}
	perms, _ := readJSON(t, file)["permissions"].(map[string]any)
	allow, _ := perms["allow"].([]any)
	if len(allow) != 1 || allow[0] != "bash(go test*)" {
		t.Fatalf("allow = %v", perms["allow"])
	}
}

func TestSetSettingPathRejectsABadPath(t *testing.T) {
	cfg, _ := userConfig(t, "")
	if err := cfg.SetUserSettingPath("audio..language", "id"); err == nil {
		t.Fatal("an empty path segment should be refused")
	}
}
