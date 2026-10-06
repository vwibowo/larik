package config

import (
	"os"
	"path/filepath"
	"testing"
)

const testITermColors = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Foreground Color</key><dict><key>Red Component</key><real>0.9</real><key>Green Component</key><real>0.8</real><key>Blue Component</key><real>0.7</real></dict>
<key>Ansi 1 Color</key><dict><key>Red Component</key><real>1</real><key>Green Component</key><real>0</real><key>Blue Component</key><real>0.5</real></dict>
</dict></plist>`

func TestParseITermColors(t *testing.T) {
	p, err := parseITermColors([]byte(testITermColors))
	if err != nil {
		t.Fatal(err)
	}
	if p.Foreground != "#E6CCB3" || p.ANSI[1] != "#FF0080" {
		t.Fatalf("unexpected palette: %+v", p)
	}
}

func TestThemeNamesAndParseTheme(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "larik", "themes")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "Test Scheme.itermcolors"), []byte(testITermColors), 0o600); err != nil {
		t.Fatal(err)
	}
	names := ThemeNames()
	if len(names) != 1 || names[0] != "Test Scheme" {
		t.Fatalf("names = %#v", names)
	}
	if got, err := ParseTheme("Test Scheme"); err != nil || got != "Test Scheme" {
		t.Fatalf("parse installed theme = %q, %v", got, err)
	}
	if _, err := ParseTheme("missing"); err == nil {
		t.Fatal("missing theme should fail")
	}
}

func TestParseITermColorsRejectsEmpty(t *testing.T) {
	if _, err := parseITermColors([]byte(`<plist><dict><key>Name</key><string>empty</string></dict></plist>`)); err == nil {
		t.Fatal("empty palette should fail")
	}
}
