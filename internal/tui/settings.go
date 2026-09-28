package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"larik/internal/config"
	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/providers"
)

// settingsPanel is the /config screen: a list of settings, and for the one
// being changed, its values or a text field. Changes are saved to the user
// config (~/.config/larik/config.json) and applied to this session right
// away.
type settingsPanel struct {
	list    *picker
	editing string           // key of the setting being changed
	values  *picker          // its values, for a choice
	input   *textinput.Model // its text, for a text setting
	status  string           // result of the last change
	failed  bool
	// themeWas restores the theme when a previewed choice is abandoned.
	themeWas string
}

type settingKind int

const (
	kindChoice settingKind = iota
	kindToggle
	kindText
	kindAction // opens another screen
)

type settingChoice struct {
	value, label, desc string
	warn               bool
}

// settingSpec describes one row of /config and how to change it.
type settingSpec struct {
	key, title, section string
	kind                settingKind
	choices             []settingChoice
	placeholder         string // for text settings
	later               string // said after saving when it doesn't apply at once
	// get returns the current value: a choice value, "on"/"off", or text.
	get func(m *model) string
	// store turns a checked value into what the config file holds; nil
	// removes the key so the built-in default applies.
	store func(v string) any
	// set applies a checked value to the config and this session.
	set func(m *model, v string)
	// other, for a choice, accepts a typed value that isn't one of the
	// choices (e.g. any number of days), returning it normalized.
	other func(v string) (string, bool)
	// For an action: the command that changes it, and how to open its screen.
	cmd  string
	open func(m *model) tea.Cmd
}

func toggle(key, title, section string, get func(c *config.Config) bool, set func(m *model, on bool)) settingSpec {
	return settingSpec{
		key: key, title: title, section: section, kind: kindToggle,
		get:   func(m *model) string { return onOff(get(m.opts.Config)) },
		store: func(v string) any { return v == "on" },
		set:   func(m *model, v string) { set(m, v == "on") },
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// orEmpty stores v, or removes the key when v is the default.
func orEmpty(def string) func(string) any {
	return func(v string) any {
		if v == def {
			return nil
		}
		return v
	}
}

var settingSpecs = []settingSpec{
	{
		key: "theme", title: "Theme", section: "appearance", kind: kindChoice,
		choices: []settingChoice{
			{value: "auto", label: "Auto", desc: "follow the terminal's background"},
			{value: "dark", label: "Dark", desc: "colors for a dark background"},
			{value: "light", label: "Light", desc: "colors for a light background"},
		},
		get:   func(m *model) string { return m.theme() },
		store: orEmpty("auto"),
		set: func(m *model, v string) {
			if v == "auto" {
				v = ""
			}
			m.previewTheme(v)
		},
	},
	toggle("verbose", "Verbose output", "appearance",
		(*config.Config).VerboseOn,
		func(m *model, on bool) {
			m.opts.Config.Verbose = &on
			m.verbose, m.showThinking = on, on
		}),
	toggle("spinner_tips", "Spinner tips", "appearance",
		(*config.Config).TipsOn,
		func(m *model, on bool) {
			m.opts.Config.SpinnerTips = &on
			m.tips = on
		}),
	toggle("auto_compact", "Auto-compact", "behavior",
		(*config.Config).AutoCompactOn,
		func(m *model, on bool) {
			m.opts.Config.AutoCompact = &on
			m.agent.SetAutoCompact(on)
		}),
	{
		key: "notifications", title: "Notifications", section: "behavior", kind: kindChoice,
		choices: []settingChoice{
			{value: "off", label: "Off"},
			{value: "bell", label: "Terminal bell", desc: "when larik needs you and you're elsewhere"},
			{value: "desktop", label: "Desktop", desc: "system notification; the bell if unsupported"},
		},
		get:   func(m *model) string { return m.notify },
		store: orEmpty("off"),
		set: func(m *model, v string) {
			m.notify, m.opts.Config.Notifications = v, v
			if v == "off" {
				m.opts.Config.Notifications = ""
			}
		},
	},
	{
		key: "language", title: "Response language", section: "behavior", kind: kindText,
		placeholder: "e.g. Indonesian; empty lets the model choose",
		later:       "applies after /clear or in a new session",
		get:         func(m *model) string { return m.opts.Config.Language },
		store:       orEmpty(""),
		set: func(m *model, v string) {
			m.opts.Config.Language = v
			m.agent.SetLanguage(v)
		},
	},
	{
		key: "checkpoint_retention_days", title: "Undo history", section: "behavior", kind: kindChoice,
		choices: []settingChoice{
			{value: "1", label: "1 day"},
			{value: "7", label: "7 days", desc: "the default"},
			{value: "30", label: "30 days"},
			{value: "90", label: "90 days"},
			{value: "forever", label: "Forever", desc: "never delete old snapshots"},
		},
		later: "old snapshots are deleted when larik starts",
		get: func(m *model) string {
			switch d := m.opts.Config.CheckpointRetentionDays; {
			case d < 0:
				return "forever"
			case d == 0:
				return "7"
			default:
				return strconv.Itoa(d)
			}
		},
		store: func(v string) any {
			switch v {
			case "7":
				return nil
			case "forever":
				return -1
			}
			n, _ := strconv.Atoi(v)
			return n
		},
		set: func(m *model, v string) {
			switch v {
			case "7":
				m.opts.Config.CheckpointRetentionDays = 0
			case "forever":
				m.opts.Config.CheckpointRetentionDays = -1
			default:
				m.opts.Config.CheckpointRetentionDays, _ = strconv.Atoi(v)
			}
		},
		other: func(v string) (string, bool) {
			n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(v, "days")), "d"))
			if err != nil || n < 1 {
				return "", false
			}
			return strconv.Itoa(n), true
		},
	},
	{
		key: "mode", title: "Permission mode", section: "defaults", kind: kindChoice,
		choices: []settingChoice{
			{value: "default", label: modeLabels[permission.ModeDefault], desc: "ask before edits and commands"},
			{value: "accept-edits", label: modeLabels[permission.ModeAcceptEdits], desc: "edit project files freely, ask before commands"},
			{value: "plan", label: modeLabels[permission.ModePlan], desc: "read-only: explore and propose"},
			{value: "yolo", label: modeLabels[permission.ModeYolo], desc: "run everything without asking", warn: true},
		},
		get: func(m *model) string {
			if md := m.opts.Config.Mode; md != "" {
				return string(md)
			}
			return "default"
		},
		store: orEmpty("default"),
		set: func(m *model, v string) {
			md, _ := permission.ParseMode(v)
			m.opts.Config.Mode = md
			if v == "default" {
				m.opts.Config.Mode = ""
			}
			m.agent.Perms().SetMode(md)
		},
	},
	{
		key: "effort", title: "Reasoning effort", section: "defaults", kind: kindChoice,
		choices: []settingChoice{
			{value: "default", label: "Provider default", desc: "let the model decide"},
			{value: "low", label: "Low"},
			{value: "medium", label: "Medium"},
			{value: "high", label: "High"},
			{value: "xhigh", label: "Extra high"},
			{value: "max", label: "Max"},
		},
		get: func(m *model) string {
			if e := m.opts.Config.Effort; e != "" {
				return string(e)
			}
			return "default"
		},
		store: orEmpty("default"),
		set: func(m *model, v string) {
			e := llm.Effort(v)
			if v == "default" {
				e = llm.EffortDefault
			}
			m.opts.Config.Effort = e
			m.agent.SetEffort(e)
		},
	},
	{
		key: "model", title: "Model", section: "defaults", kind: kindAction,
		get:  func(m *model) string { return m.opts.Config.Model },
		cmd:  "/model",
		open: func(m *model) tea.Cmd { return m.openModelPicker() },
	},
	{
		key: "routing", title: "Model routing", section: "defaults", kind: kindAction,
		get: func(m *model) string {
			var set []string
			r := m.opts.Config.Routing()
			for _, n := range providers.RoleNames(m.opts.Config) {
				if spec := r.Roles[n]; spec != "" {
					set = append(set, n+" "+spec)
				}
			}
			return strings.Join(set, ", ")
		},
		cmd:  "/routing",
		open: func(m *model) tea.Cmd { return m.openRouting(rtPreset) },
	},
	{
		key: "budget", title: "Session budget", section: "defaults", kind: kindAction,
		get: func(m *model) string {
			if b := m.opts.Config.Routing().Budget; b.SessionUSD > 0 {
				return fmt.Sprintf("$%.2f", b.SessionUSD)
			}
			return ""
		},
		cmd:  "/routing budget=",
		open: func(m *model) tea.Cmd { return m.openRouting(rtBudget) },
	},
}

func settingByKey(key string) (settingSpec, bool) {
	key = strings.ReplaceAll(strings.ToLower(key), "-", "_")
	for _, s := range settingSpecs {
		if s.key == key {
			return s, true
		}
	}
	return settingSpec{}, false
}

func (s settingSpec) label(v string) string {
	for _, c := range s.choices {
		if c.value == v {
			return c.label
		}
	}
	if s.other != nil {
		if n, ok := s.other(v); ok {
			return n + " days"
		}
	}
	return v
}

// check validates a value typed for s and normalizes it.
func (s settingSpec) check(v string) (string, error) {
	v = strings.TrimSpace(v)
	switch s.kind {
	case kindToggle:
		switch strings.ToLower(v) {
		case "on", "true", "yes", "1":
			return "on", nil
		case "off", "false", "no", "0":
			return "off", nil
		}
		return "", fmt.Errorf("%s is on or off", s.key)
	case kindChoice:
		var names []string
		for _, c := range s.choices {
			if strings.EqualFold(v, c.value) {
				return c.value, nil
			}
			names = append(names, c.value)
		}
		if s.other != nil {
			if n, ok := s.other(v); ok {
				return n, nil
			}
			return "", fmt.Errorf("%s is a number of days, or forever", s.key)
		}
		return "", fmt.Errorf("%s is one of %s", s.key, strings.Join(names, ", "))
	case kindAction:
		return "", fmt.Errorf("use %s to change the %s", s.cmd, s.key)
	}
	return v, nil
}

// saveSetting stores a setting in the user config and applies it to this
// session, returning what to tell the user.
func (m *model) saveSetting(key, raw string) (string, error) {
	spec, ok := settingByKey(key)
	if !ok {
		var keys []string
		for _, s := range settingSpecs {
			keys = append(keys, s.key)
		}
		return "", fmt.Errorf("unknown setting %q (settings: %s)", key, strings.Join(keys, ", "))
	}
	v, err := spec.check(raw)
	if err != nil {
		return "", err
	}
	if err := m.opts.Config.SetUserSetting(spec.key, spec.store(v)); err != nil {
		return "", fmt.Errorf("couldn't save: %w", err)
	}
	spec.set(m, v)
	shown := spec.label(v)
	if spec.kind == kindText && v == "" {
		shown = "not set"
	}
	msg := spec.title + " set to " + shown + " · saved to " + shortHome(m.opts.Config.UserConfigPath())
	if spec.later != "" {
		msg += " · " + spec.later
	}
	return msg, nil
}

// configCommand handles /config key=value (or "key value").
func (m *model) configCommand(arg string) tea.Cmd {
	key, val, ok := strings.Cut(arg, "=")
	if !ok {
		key, val, _ = strings.Cut(arg, " ")
	}
	msg, err := m.saveSetting(strings.TrimSpace(key), val)
	if err != nil {
		return m.println(m.st.err.Render(err.Error()))
	}
	return m.println(m.st.dim.Render(msg))
}

// openSettings shows the /config screen, jumping straight to one
// setting's values when key is set.
func (m *model) openSettings(key string) tea.Cmd {
	m.settings = &settingsPanel{}
	m.buildSettings()
	if key != "" {
		m.settings.list.selectWhere(func(it pickItem) bool { return it.value == key })
		return m.editSetting(key)
	}
	return nil
}

func (m *model) buildSettings() {
	cfg := m.opts.Config
	p := &picker{}
	for _, spec := range settingSpecs {
		v := spec.get(m)
		it := pickItem{section: spec.section, label: spec.title, value: spec.key}
		switch spec.kind {
		case kindToggle:
			it.detail = v
		case kindText:
			it.detail = v
			if v == "" {
				it.detail = "not set"
			}
		case kindAction:
			it.detail = v
			if v == "" {
				it.detail = "not set"
			}
			it.note = spec.cmd
		default:
			it.detail = spec.label(v)
		}
		if spec.key == "theme" && v == "auto" {
			bg := "dark"
			if !m.termDark {
				bg = "light"
			}
			it.detail += " (" + bg + " terminal)"
		}
		p.items = append(p.items, it)
	}

	files := []struct{ path, label, what string }{
		{cfg.UserConfigPath(), shortHome(cfg.UserConfigPath()), "yours, all projects · /config saves here"},
		{filepath.Join(cfg.Cwd, ".larik", "settings.json"), ".larik/settings.json", "this project, shared"},
		{config.LocalSettingsPath(cfg.Cwd), "private project settings", "this project, yours"},
	}
	for _, f := range files {
		note := "not created"
		if _, err := os.Stat(f.path); err == nil {
			note = "exists"
		}
		p.items = append(p.items, pickItem{section: "files (later override earlier)", label: f.label, detail: f.what, note: note, disabled: true})
	}
	p.home()
	if old := m.settings.list; old != nil {
		if it, ok := old.selected(); ok {
			p.selectWhere(func(n pickItem) bool { return n.value == it.value })
		}
	}
	m.settings.list = p
}

func shortHome(path string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, path); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return filepath.Join("~", rel)
		}
	}
	return path
}

// editSetting starts changing a setting: a toggle flips at once, a
// choice shows its values, text opens a field.
func (m *model) editSetting(key string) tea.Cmd {
	s := m.settings
	spec, _ := settingByKey(key)
	cur := spec.get(m)
	switch spec.kind {
	case kindAction:
		m.settings = nil
		if m.running && spec.key == "model" {
			return m.println(m.st.err.Render("the model can't change while a turn is running (esc to interrupt)"))
		}
		return spec.open(m)
	case kindToggle:
		next := "on"
		if cur == "on" {
			next = "off"
		}
		m.finishEdit(key, next)
		return nil
	case kindText:
		ti := textinput.New()
		ti.Prompt = "› "
		ti.Placeholder = spec.placeholder
		ti.SetValue(cur)
		ti.SetWidth(max(m.width-10, 20))
		s.editing, s.input = key, &ti
		return ti.Focus()
	}
	p := &picker{}
	for _, c := range spec.choices {
		it := pickItem{label: c.label, detail: c.desc, value: c.value, warn: c.warn}
		if c.value == cur {
			it.note, it.noteOK = "✓ current", true
		}
		p.items = append(p.items, it)
	}
	p.selectWhere(func(it pickItem) bool { return it.value == cur })
	s.editing, s.values, s.themeWas = key, p, m.opts.Config.Theme
	return nil
}

// finishEdit saves a value and goes back to the list.
func (m *model) finishEdit(key, value string) {
	s := m.settings
	if key == "theme" {
		m.previewTheme(s.themeWas) // so a failed save leaves the old theme
	}
	msg, err := m.saveSetting(key, value)
	if err != nil {
		s.status, s.failed = err.Error(), true
	} else {
		s.status, s.failed = msg, false
	}
	s.editing, s.values, s.input = "", nil, nil
	m.buildSettings()
}

func (m *model) handleSettingsKey(msg tea.KeyPressMsg) tea.Cmd {
	s := m.settings
	k := msg.String()
	switch {
	case s.input != nil:
		switch k {
		case "esc", "ctrl+c":
			s.editing, s.input = "", nil
			return nil
		case "enter":
			m.finishEdit(s.editing, s.input.Value())
			return nil
		}
		ti, cmd := s.input.Update(msg)
		s.input = &ti
		return cmd

	case s.values != nil:
		switch k {
		case "esc", "ctrl+c":
			if s.editing == "theme" {
				m.previewTheme(s.themeWas)
			}
			s.editing, s.values = "", nil
			return nil
		}
		if s.values.handleKey(msg) {
			it, _ := s.values.selected()
			m.finishEdit(s.editing, it.value.(string))
			return nil
		}
		if s.editing == "theme" { // show the highlighted theme as the cursor moves
			if it, ok := s.values.selected(); ok {
				m.previewTheme(it.value.(string))
			}
		}
		return nil
	}

	switch k {
	case "esc", "ctrl+c", "q":
		m.settings = nil
		return nil
	case "space", " ":
		if it, ok := s.list.selected(); ok {
			return m.editSetting(it.value.(string))
		}
		return nil
	}
	if s.list.handleKey(msg) {
		it, _ := s.list.selected()
		return m.editSetting(it.value.(string))
	}
	return nil
}

// previewTheme applies a theme without saving it; "" means auto.
func (m *model) previewTheme(theme string) {
	if theme == "auto" {
		theme = ""
	}
	m.opts.Config.Theme = theme
	if d := m.wantDark(); d != m.isDark {
		m.applyTheme(d)
	}
}

// setTheme handles /theme <name>.
func (m *model) setTheme(name string) tea.Cmd {
	return m.configCommand("theme=" + name)
}

func (m *model) settingsView() string {
	s := m.settings
	w := max(m.width-6, 20)
	var head, body, hint string
	switch {
	case s.input != nil:
		spec, _ := settingByKey(s.editing)
		head = spread(m.st.accent.Render(spec.title), m.st.dim.Render("saved for every project"), w)
		body = s.input.View()
		hint = "enter save · esc back"
	case s.values != nil:
		spec, _ := settingByKey(s.editing)
		head = spread(m.st.accent.Render(spec.title), m.st.dim.Render("saved for every project"), w)
		body = s.values.view(m.st, w)
		hint = "↑/↓ + enter choose · esc back"
	default:
		head = spread(m.st.accent.Render("Settings"), m.st.dim.Render("/config"), w)
		body = s.list.view(m.st, w)
		hint = "↑/↓ move · enter or space change · esc close"
	}
	out := head + "\n" + body
	if s.status != "" {
		st := m.st.ok
		if s.failed {
			st = m.st.err
		}
		out += "\n" + st.Render(s.status)
	}
	return m.st.modal.Width(max(m.width-2, 10)).Render(out + "\n" + m.st.dim.Render(hint))
}
