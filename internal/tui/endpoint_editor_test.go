package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/config"
)

func TestEndpointEditorsKeepPersonalLeavesAndSecretsPrivate(t *testing.T) {
	m := testModel(t)
	path := m.opts.Config.UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"web":{"fetch_disabled":true,"search":{"provider":"brave","api_key":"old-secret","extra":"stay"}},"audio":{"enabled":true,"stt":{"model":"old","api_key":"stt-secret","unknown":42},"tts":{"model":"tts-old"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ cmd, kind, field, value string }{
		{"/web-search-config", "web", "url", "https://example.org/search"},
		{"/stt-config", "stt", "language", "id"},
		{"/tts-config", "tts", "voice", "alloy"},
	} {
		m.command(tc.cmd)
		e := m.settings.endpoint
		if e == nil || e.kind != tc.kind {
			t.Fatalf("%s did not open editor", tc.cmd)
		}
		if strings.Contains(m.endpointEditorView(), "secret") {
			t.Fatal("secret shown in panel")
		}
		e.startInput("api_key", m.width)
		if strings.Contains(e.input.Value(), "secret") {
			t.Fatal("secret prefilled")
		}
		e.input = nil
		e.change(tc.field, tc.value)
		m.saveEndpointEditor()
		if m.settings == nil || !strings.Contains(e.status, "/reload") {
			t.Fatalf("expected reload notice: %q", e.status)
		}
	}
	data := savedConfig(t, m)
	for _, s := range []string{`"fetch_disabled": true`, `"extra": "stay"`, `"unknown": 42`, `"enabled": true`, `"tts-old"`, `"old-secret"`, `"stt-secret"`, `"alloy"`, `"id"`} {
		if !strings.Contains(data, s) {
			t.Errorf("missing %s: %s", s, data)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secret config mode %o", info.Mode().Perm())
	}
}

func TestEndpointEditorsReplaceClearAndDoNotCopyMergedValues(t *testing.T) {
	m := testModel(t)
	m.opts.Config.Web.Search.Provider = "tavily" // merged/shared values must not be written
	m.opts.Config.Audio.STT.Model = "project-model"
	m.command("/config web_search")
	e := m.settings.endpoint
	if e.fields["provider"] != "" {
		t.Fatal("editor read merged provider")
	}
	e.change("api_key", "new-secret")
	m.saveEndpointEditor()
	if strings.Contains(savedConfig(t, m), "tavily") {
		t.Fatal("copied merged provider")
	}
	m.command("/web-search-config")
	e = m.settings.endpoint
	if !strings.Contains(m.endpointEditorView(), "set · replace or clear") {
		t.Fatal("key not masked")
	}
	e.change("api_key", "")
	m.saveEndpointEditor()
	if strings.Contains(savedConfig(t, m), "new-secret") {
		t.Fatal("key not cleared")
	}
	m.command("/config audio_stt")
	if m.settings.endpoint == nil || m.settings.endpoint.fields["model"] != "" {
		t.Fatal("copied merged STT model")
	}
	m.command("/config audio_tts")
	if m.settings.endpoint == nil || m.settings.endpoint.kind != "tts" {
		t.Fatal("TTS action not opened")
	}
}

func TestEndpointEditorKeyboardChoicesAndMaskedReplacement(t *testing.T) {
	m := testModel(t)
	m.command("/web-search-config")
	e := m.settings.endpoint
	e.list.selectWhere(func(it pickItem) bool { return it.value == "provider" })
	m.Update(press(tea.KeyEnter))
	if e.fields["provider"] != "brave" {
		t.Fatalf("provider = %q", e.fields["provider"])
	}
	e.list.selectWhere(func(it pickItem) bool { return it.value == "disabled" })
	m.Update(press(tea.KeyEnter))
	if e.changes["web.search.disabled"] != true {
		t.Fatal("disabled toggle not staged")
	}
	e.list.selectWhere(func(it pickItem) bool { return it.value == "url" })
	m.Update(press(tea.KeyEnter))
	e.input.SetValue("ftp://invalid")
	m.Update(press(tea.KeyEnter))
	if !e.failed || e.input == nil {
		t.Fatal("invalid URL accepted")
	}
	m.Update(press(tea.KeyEscape))
	e.list.selectWhere(func(it pickItem) bool { return it.value == "api_key" })
	m.Update(press(tea.KeyEnter))
	e.input.SetValue("hidden-value")
	if strings.Contains(e.input.View(), "hidden-value") {
		t.Fatal("new secret shown while typing")
	}
	m.Update(press(tea.KeyEnter))
	e.list.selectWhere(func(it pickItem) bool { return it.value == "clear_key" })
	m.Update(press(tea.KeyEnter))
	if e.fields["api_key"] != "" {
		t.Fatal("clear action did not clear secret")
	}
	m.Update(typedKey('s'))
	data := savedConfig(t, m)
	if strings.Contains(data, "hidden-value") || !strings.Contains(data, `"disabled": true`) || !strings.Contains(data, `"provider": "brave"`) {
		t.Fatalf("wrong saved search: %s", data)
	}
}

func TestEndpointEditorDoesNotOverwriteUnreadableConfig(t *testing.T) {
	m := testModel(t)
	path := m.opts.Config.UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"web":`), 0o600); err != nil {
		t.Fatal(err)
	}
	m.command("/web-search-config")
	if !m.settings.endpoint.readFailed {
		t.Fatal("invalid personal config should fail to load")
	}
	m.settings.endpoint.change("provider", "brave")
	m.saveEndpointEditor()
	if got := savedConfig(t, m); got != `{"web":` {
		t.Fatalf("corrupt config overwritten: %s", got)
	}
}

func TestAudioEndpointHelperRejectsUnknownKind(t *testing.T) {
	if _, err := config.AudioEndpointAt("", "other"); err == nil {
		t.Fatal("expected unknown endpoint error")
	}
}
