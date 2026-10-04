package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSetBrowserEnabledPreservesOtherSettings(t *testing.T) {
	cfg := &Config{ConfigDir: t.TempDir()}
	if cfg.Browser.Enabled {
		t.Fatal("browser must be off by default")
	}
	if err := os.WriteFile(cfg.UserConfigPath(), []byte(`{"browser":{"headless":true,"chrome_path":"/chrome"},"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{true, false} {
		if err := cfg.SetBrowserEnabled(enabled); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(cfg.ConfigDir, "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Browser BrowserConfig `json:"browser"`
			Theme   string        `json:"theme"`
		}
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got.Browser.Enabled != enabled || !got.Browser.Headless || got.Browser.ChromePath != "/chrome" || got.Theme != "dark" {
			t.Fatalf("enabled=%v: %+v", enabled, got)
		}
	}
}
