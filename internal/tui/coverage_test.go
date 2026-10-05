package tui

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"larik/internal/config"
)

// Larik is meant to be usable without a text editor open beside it: every
// setting it reads should be changeable from the TUI. This test walks the
// settings struct and insists that each one is reachable — from a /config
// row, from a panel that manages its section, or from the backlog below with
// a reason attached. Adding a setting without a way to change it then fails
// here rather than being discovered by someone hand-editing JSON.

// panelCoverage maps a setting, or a section of them, to the command that
// changes it. Only panels that genuinely write the setting belong here: a
// command that merely prints the section does not make it reachable.
var panelCoverage = map[string]string{
	"providers":              "/providers and /connect add, edit, test and remove them",
	"model_execution":        "/execution, per model",
	"approved_mcp_servers":   "/mcp approve",
	"approved_project_hooks": "/hooks approve",
	"browser.enabled":        "/browser on|off",
	"audio.stt.language":     "/stt-language",
}

// jsonOnly is the backlog: settings that still need a text editor. Each entry
// says what would change that, so the list reads as work rather than as an
// excuse. Shrinking it is the point; entries are removed as the panels land.
var jsonOnly = map[string]string{
	"verbose":               "superseded by appearance; kept only so old configs keep working",
	"status_line":           "a /statusline wizard, with a preview of the command's output",
	"sidebar":               "the same wizard as status_line",
	"keybindings":           "an editor built into the /keys overlay",
	"models":                "catalog overrides; rare and deeply nested, so the settings-file editor row",
	"permissions":           "a /permissions panel for allow and deny rules",
	"mcp_servers":           "a /mcp panel that adds, edits and removes servers",
	"hooks":                 "a /hooks panel that adds and edits them",
	"web.search":            "a search-backend wizard",
	"audio.stt.base_url":    "an endpoint wizard for speech",
	"audio.stt.api_key":     "an endpoint wizard for speech",
	"audio.stt.api_key_env": "an endpoint wizard for speech",
	"audio.stt.model":       "an endpoint wizard for speech",
	"audio.stt.voice":       "an endpoint wizard for speech",
	"audio.tts":             "an endpoint wizard for speech",
	"sandbox":               "a /sandbox panel: the switches, and the writable and domain lists",
	"lsp":                   "an /lsp panel that enables, disables and edits servers",
}

// configPaths lists the JSON path of every setting in t. It descends into a
// section, which is a struct, and stops at a map or a list, which is a
// collection a panel manages whole rather than a setting of its own.
func configPaths(t reflect.Type, prefix string) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue // not a setting: resolved paths, and hooks split by trust
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			out = append(out, configPaths(ft, path)...)
			continue
		}
		out = append(out, path)
	}
	return out
}

// under reports whether path is section itself or a setting inside it.
// Matching whole segments keeps "mode" from claiming "model".
func under(path, section string) bool {
	return path == section || strings.HasPrefix(path, section+".")
}

// uiCoverage maps every setting the TUI can change to how it is changed.
func uiCoverage() map[string]string {
	covered := map[string]string{}
	for _, s := range settingSpecs {
		how := "/config " + s.key
		if s.kind == kindAction {
			how = s.cmd
		}
		for _, p := range append([]string{s.configPath()}, s.covers...) {
			covered[p] = how
		}
	}
	for p, how := range panelCoverage {
		covered[p] = how
	}
	return covered
}

func TestEverySettingIsReachableFromTheTUI(t *testing.T) {
	covered := uiCoverage()
	paths := configPaths(reflect.TypeOf(config.Config{}), "")
	if len(paths) < 50 {
		t.Fatalf("only %d settings found; the walk is not seeing the config", len(paths))
	}
	used := map[string]bool{}
	var missing []string
	for _, path := range paths {
		found := false
		for section := range covered {
			if under(path, section) {
				used[section], found = true, true
				break
			}
		}
		if found {
			continue
		}
		for section := range jsonOnly {
			if under(path, section) {
				used[section], found = true, true
				break
			}
		}
		if !found {
			missing = append(missing, path)
		}
	}
	sort.Strings(missing)
	for _, path := range missing {
		t.Errorf("%s cannot be changed from the TUI: add a /config row or a panel for it, "+
			"or add it to jsonOnly with the reason", path)
	}

	// An entry that matches nothing is stale: the setting was renamed or
	// removed, and the note about it is now misleading.
	for _, list := range []map[string]string{covered, jsonOnly} {
		for section := range list {
			if used[section] {
				continue
			}
			if _, isSpec := settingByKey(section); isSpec {
				continue // a /config row whose key is not a path of its own
			}
			t.Errorf("%q matches no setting; remove it", section)
		}
	}
}

// The backlog and the finished work must not overlap: an entry left in
// jsonOnly after its panel lands would hide the next gap behind it.
func TestBacklogDropsWhatTheTUIAlreadyChanges(t *testing.T) {
	for section := range uiCoverage() {
		if reason, listed := jsonOnly[section]; listed {
			t.Errorf("%s is changeable from the TUI now; drop it from jsonOnly (%q)", section, reason)
		}
	}
	for section := range jsonOnly {
		for covered := range uiCoverage() {
			if section != covered && under(section, covered) {
				t.Errorf("%s sits inside %s, which the TUI already changes", section, covered)
			}
		}
	}
}

// Every row has to be saveable: a path that does not name a real setting
// would be written to the config file and then ignored on the next start.
func TestSettingRowsNameRealSettings(t *testing.T) {
	paths := map[string]bool{}
	for _, p := range configPaths(reflect.TypeOf(config.Config{}), "") {
		paths[p] = true
		for section := p; strings.Contains(section, "."); {
			section = section[:strings.LastIndex(section, ".")]
			paths[section] = true
		}
	}
	for _, s := range settingSpecs {
		if s.kind == kindAction {
			continue // actions open another screen; they save nothing themselves
		}
		if !paths[s.configPath()] {
			t.Errorf("/config %s writes %q, which is not a setting", s.key, s.configPath())
		}
	}
}
