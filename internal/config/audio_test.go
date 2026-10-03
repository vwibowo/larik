package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedAudioCannotEnable(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(cwd, ".larik"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".larik", "settings.json"), []byte(`{"audio":{"enabled":true,"stt":{"base_url":"http://project","model":"asr"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Audio.Enabled || cfg.Audio.STT.BaseURL != "" {
		t.Fatalf("shared audio widened access: %+v", cfg.Audio)
	}
}

func TestSetSTTLanguagePreservesAudioSettings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(configDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"audio":{"enabled":true,"auto_speak":true,"stt":{"model":"asr"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetSTTLanguage("id"); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(updated)
	for _, want := range []string{`"enabled": true`, `"auto_speak": true`, `"model": "asr"`, `"language": "id"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("updated config missing %s: %s", want, text)
		}
	}
}

func TestPersonalAudioLoads(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(configDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir(), "config.json"), []byte(`{"audio":{"enabled":true,"stt":{"base_url":"http://127.0.0.1:8000/v1","model":"asr"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Audio.Enabled || cfg.Audio.STT.Model != "asr" {
		t.Fatalf("personal audio did not load: %+v", cfg.Audio)
	}
}
