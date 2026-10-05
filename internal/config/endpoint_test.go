package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersonalEndpointHelpersAndAtomicPatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"web":{"search":{"provider":"brave","unknown":1},"fetch_disabled":true},"audio":{"stt":{"model":"old"},"tts":{"voice":"old"},"enabled":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	search, err := WebSearchAt(path)
	if err != nil || search.Provider != "brave" {
		t.Fatalf("search = %+v, %v", search, err)
	}
	stt, err := AudioEndpointAt(path, "stt")
	if err != nil || stt.Model != "old" {
		t.Fatalf("stt = %+v, %v", stt, err)
	}
	if err := SetSettingPath(path, "web.search.api_key", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := SetSettingPath(path, "audio.stt.language", "id"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`"unknown": 1`, `"fetch_disabled": true`, `"voice": "old"`, `"enabled": true`, `"api_key": "secret"`} {
		if !strings.Contains(string(data), s) {
			t.Errorf("lost %s in %s", s, data)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
}
