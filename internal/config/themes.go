package config

import (
	"encoding/xml"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ThemePalette is the color data exposed by an iTerm2 color scheme.
// Colors are CSS-style six-digit hex strings.
type ThemePalette struct {
	Name       string
	Foreground string
	Background string
	Cursor     string
	ANSI       [16]string
}

// ThemeDir is the personal directory where .itermcolors files are installed.
func ThemeDir() string { return filepath.Join(configDir(), "themes") }

// ThemeNames returns installed iTerm2 scheme names in stable order.
func ThemeNames() []string {
	entries, err := os.ReadDir(ThemeDir())
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".itermcolors") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		if _, err := LoadTheme(name); err == nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// LoadTheme loads an installed iTerm2 scheme by its filename without extension.
func LoadTheme(name string) (ThemePalette, error) {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return ThemePalette{}, fmt.Errorf("invalid theme name %q", name)
	}
	path := filepath.Join(ThemeDir(), name+".itermcolors")
	data, err := os.ReadFile(path)
	if err != nil {
		return ThemePalette{}, fmt.Errorf("theme %q is not installed in %s", name, ThemeDir())
	}
	p, err := parseITermColors(data)
	if err != nil {
		return ThemePalette{}, fmt.Errorf("theme %q: %w", name, err)
	}
	p.Name = name
	return p, nil
}

// ParseTheme accepts built-in themes and installed iTerm2 schemes.
func ParseTheme(s string) (string, error) {
	if s == "" {
		return "auto", nil
	}
	for _, t := range []string{"auto", "dark", "light"} {
		if s == t {
			return s, nil
		}
	}
	if _, err := LoadTheme(s); err == nil {
		return s, nil
	}
	return "", fmt.Errorf("unknown theme %q (want auto, dark, light, or an installed .itermcolors theme)", s)
}

type plistNode struct {
	kind, text string
	kids       map[string]plistNode
}

func parseITermColors(data []byte) (ThemePalette, error) {
	var root plistNode
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	for {
		t, err := dec.Token()
		if err != nil {
			return ThemePalette{}, fmt.Errorf("invalid XML: %w", err)
		}
		if start, ok := t.(xml.StartElement); ok && start.Name.Local == "dict" {
			root, err = readPlistDict(dec)
			if err != nil {
				return ThemePalette{}, err
			}
			break
		}
	}
	p := ThemePalette{Foreground: colorNode(root, "Foreground Color"), Background: colorNode(root, "Background Color"), Cursor: colorNode(root, "Cursor Color")}
	for i := range p.ANSI {
		p.ANSI[i] = colorNode(root, fmt.Sprintf("Ansi %d Color", i))
		if p.ANSI[i] == "" {
			p.ANSI[i] = colorNode(root, fmt.Sprintf("ANSI %d Color", i))
		}
	}
	if p.Foreground == "" && p.Background == "" && p.ANSI[0] == "" {
		return ThemePalette{}, fmt.Errorf("contains no colors")
	}
	return p, nil
}

func readPlistDict(dec *xml.Decoder) (plistNode, error) {
	n := plistNode{kind: "dict", kids: map[string]plistNode{}}
	var key string
	for {
		t, err := dec.Token()
		if err != nil {
			return n, err
		}
		switch v := t.(type) {
		case xml.EndElement:
			if v.Name.Local == "dict" {
				return n, nil
			}
		case xml.StartElement:
			switch v.Name.Local {
			case "key":
				if err := dec.DecodeElement(&key, &v); err != nil {
					return n, err
				}
			case "string", "real", "integer":
				var value string
				if err := dec.DecodeElement(&value, &v); err != nil {
					return n, err
				}
				n.kids[key] = plistNode{kind: v.Name.Local, text: value}
			case "dict":
				child, err := readPlistDict(dec)
				if err != nil {
					return n, err
				}
				n.kids[key] = child
			}
		}
	}
}

func colorNode(root plistNode, key string) string {
	n, ok := root.kids[key]
	if !ok || n.kind != "dict" {
		return ""
	}
	read := func(k string) (float64, bool) {
		v, ok := n.kids[k]
		if !ok {
			return 0, false
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(v.text), 64)
		return f, err == nil
	}
	r, rok := read("Red Component")
	g, gok := read("Green Component")
	b, bok := read("Blue Component")
	if !rok || !gok || !bok {
		return ""
	}
	component := func(v float64) int { return max(0, min(255, int(math.Round(v*255)))) }
	return fmt.Sprintf("#%02X%02X%02X", component(r), component(g), component(b))
}
