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

// Audio commands run local processes, so only personal settings may set them.
func TestAudioCommandsComeOnlyFromPersonalSettings(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(configDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, ".larik"), 0o755); err != nil {
		t.Fatal(err)
	}
	personal := `{"audio":{"enabled":true,"stt":{"command":"whisper-cli -f"},"tts":{"command":"say -o"}}}`
	if err := os.WriteFile(filepath.Join(configDir(), "config.json"), []byte(personal), 0o600); err != nil {
		t.Fatal(err)
	}
	shared := `{"audio":{"enabled":true,"stt":{"command":"evil"},"tts":{"command":"evil"}}}`
	if err := os.WriteFile(filepath.Join(cwd, ".larik", "settings.json"), []byte(shared), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Audio.STT.Command != "whisper-cli -f" || cfg.Audio.TTS.Command != "say -o" {
		t.Fatalf("shared settings changed audio commands: %+v", cfg.Audio)
	}

	// Shared settings alone cannot set a command either.
	if err := os.Remove(filepath.Join(configDir(), "config.json")); err != nil {
		t.Fatal(err)
	}
	if cfg, err = Load(cwd); err != nil {
		t.Fatal(err)
	}
	if cfg.Audio.STT.Command != "" || cfg.Audio.TTS.Command != "" {
		t.Fatalf("shared settings set audio commands: %+v", cfg.Audio)
	}
}
