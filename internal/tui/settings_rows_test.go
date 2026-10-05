package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Each row has four pieces that have to agree: what it reads, what it writes
// to the file, what it applies to the session, and what it reads back. A
// mismatch between any two shows up as a setting that silently reverts, so
// every row is exercised rather than the handful someone thought to test.

// Saving the value a setting already has must leave it at that value.
func TestEveryRowRoundTripsItsCurrentValue(t *testing.T) {
	for _, spec := range settingSpecs {
		if spec.kind == kindAction {
			continue // actions open a screen; they store nothing themselves
		}
		t.Run(spec.key, func(t *testing.T) {
			m := testModel(t)
			before := spec.get(m)
			v, err := spec.check(before)
			if err != nil {
				t.Fatalf("%s reads %q, which it then rejects: %v", spec.key, before, err)
			}
			if _, _, err := m.saveSetting(spec.key, v); err != nil {
				t.Fatalf("saving the current value failed: %v", err)
			}
			if after := spec.get(m); after != before {
				t.Errorf("%s changed itself by being saved: %q became %q", spec.key, before, after)
			}
		})
	}
}

// Every value a row offers must survive being chosen: accepted, written, and
// read back as itself.
func TestEveryChoiceRoundTrips(t *testing.T) {
	for _, spec := range settingSpecs {
		for _, c := range spec.choices {
			t.Run(spec.key+"="+c.value, func(t *testing.T) {
				m := testModel(t)
				v, err := spec.check(c.value)
				if err != nil {
					t.Fatalf("%s offers %q but rejects it: %v", spec.key, c.value, err)
				}
				if v != c.value {
					t.Fatalf("%s normalised its own choice %q to %q", spec.key, c.value, v)
				}
				if _, _, err := m.saveSetting(spec.key, c.value); err != nil {
					t.Fatalf("saving %q failed: %v", c.value, err)
				}
				if after := spec.get(m); after != c.value {
					t.Errorf("%s set to %q reads back as %q", spec.key, c.value, after)
				}
			})
		}
	}
}

// A row that writes a whole section has to leave the rest of it alone: the
// audio settings are one object, and a switch must not take the endpoints
// with it.
func TestSettingInsideASectionKeepsTheRest(t *testing.T) {
	m := testModel(t)
	if err := m.opts.Config.SetUserSettingPath("audio.stt.model", "whisper-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.saveSetting("audio_auto_speak", "on"); err != nil {
		t.Fatal(err)
	}
	saved := savedConfig(t, m)
	if !strings.Contains(saved, `"whisper-1"`) || !strings.Contains(saved, `"auto_speak": true`) {
		t.Fatalf("the audio section lost part of itself: %s", saved)
	}
}

// web.fetch_disabled names what is switched off, so that a file saying
// nothing leaves fetching on. The row shows the switch the other way round,
// and the two must not drift apart.
func TestWebFetchRowStoresTheInverse(t *testing.T) {
	m := testModel(t)
	if got := settingValue(t, m, "web_fetch"); got != "on" {
		t.Fatalf("web fetch should start on, got %q", got)
	}
	if _, _, err := m.saveSetting("web_fetch", "off"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(savedConfig(t, m), `"fetch_disabled": true`) {
		t.Fatalf("switching fetch off should disable it in the file: %s", savedConfig(t, m))
	}
	if m.opts.Config.Web.FetchDisabled != true {
		t.Error("the running session still has web fetch on")
	}
	if _, _, err := m.saveSetting("web_fetch", "on"); err != nil {
		t.Fatal(err)
	}
	// A switch records what you chose either way, as the other toggles do,
	// so a change of default later cannot quietly change your setting.
	if !strings.Contains(savedConfig(t, m), `"fetch_disabled": false`) {
		t.Fatalf("switching it back on should be recorded: %s", savedConfig(t, m))
	}
	if m.opts.Config.Web.FetchDisabled {
		t.Error("the running session still has web fetch off")
	}
}

// There are more settings than fit on a screen, so the list is typed at.
// Esc backs out one step at a time rather than throwing the panel away on
// the first press.
func TestSettingsListFiltersAsYouType(t *testing.T) {
	m := testModel(t)
	m.openSettings("")
	typeText(m, "lang")
	rows := m.settings.list.visible()
	if len(rows) == 0 || len(rows) >= len(settingSpecs) {
		t.Fatalf("typing should narrow the list, got %d of %d rows", len(rows), len(settingSpecs))
	}
	for _, it := range rows {
		if !strings.Contains(strings.ToLower(it.label+" "+it.section+" "+it.detail), "lang") {
			t.Errorf("row %q does not match the filter", it.label)
		}
	}
	m.Update(press(tea.KeyEscape))
	if m.settings == nil {
		t.Fatal("the first esc should clear the filter, not close the panel")
	}
	if m.settings.list.filter != "" {
		t.Fatalf("filter should be cleared, got %q", m.settings.list.filter)
	}
	m.Update(press(tea.KeyEscape))
	if m.settings != nil {
		t.Fatal("a second esc should close the panel")
	}
}

// A setting can be found by what it is set to, not only by its name.
func TestSettingsListFindsBySavedValue(t *testing.T) {
	m := testModel(t)
	if _, _, err := m.saveSetting("editor_mode", "vim"); err != nil {
		t.Fatal(err)
	}
	m.openSettings("")
	typeText(m, "vim")
	rows := m.settings.list.visible()
	if len(rows) != 1 || rows[0].value != "editor_mode" {
		t.Fatalf("typing a value should find its setting, got %+v", rows)
	}
}

func settingValue(t *testing.T, m *model, key string) string {
	t.Helper()
	spec, ok := settingByKey(key)
	if !ok {
		t.Fatalf("no setting named %q", key)
	}
	return spec.get(m)
}

func TestNumberRowBounds(t *testing.T) {
	spec, ok := settingByKey("max_turns")
	if !ok {
		t.Fatal("max_turns is missing")
	}
	for _, bad := range []string{"0", "-5", "100000", "many", "1.5"} {
		if v, err := spec.check(bad); err == nil {
			t.Errorf("%q should be refused, became %q", bad, v)
		}
	}
	for _, good := range []string{"1", "40", "10000"} {
		if _, err := spec.check(good); err != nil {
			t.Errorf("%q should be accepted: %v", good, err)
		}
	}
	// Empty and "default" both mean "leave it out of the file".
	for _, none := range []string{"", "default", "  "} {
		v, err := spec.check(none)
		if err != nil || v != "" {
			t.Errorf("%q should clear the setting, got %q (%v)", none, v, err)
		}
	}
	if spec.store("") != nil {
		t.Error("a cleared number should remove the setting, not write zero")
	}
	if spec.label("") != "default" {
		t.Errorf("a cleared number should read as default, got %q", spec.label(""))
	}
	if spec.label("40") != "40 turns" {
		t.Errorf("a number should read with its unit, got %q", spec.label("40"))
	}
}

// A setting the built-in default already covers is stored as nothing, so the
// config file stays a record of what you chose rather than of everything.
func TestDefaultsAreStoredAsNoSetting(t *testing.T) {
	m := testModel(t)
	for key, def := range map[string]string{
		"max_turns": "200", "stall_timeout": "default", "tool_search": "auto",
		"debug_retention_days": "14", "theme": "auto", "appearance": "default",
	} {
		if _, _, err := m.saveSetting(key, def); err != nil {
			t.Fatalf("%s=%s: %v", key, def, err)
		}
		spec, _ := settingByKey(key)
		if strings.Contains(savedConfig(t, m), `"`+spec.configPath()+`"`) {
			t.Errorf("%s at its default should leave no setting: %s", key, savedConfig(t, m))
		}
	}
}
