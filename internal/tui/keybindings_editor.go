package tui

import (
	"maps"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"larik/internal/config"
)

type keybindingsEditor struct {
	bindings map[string]config.KeyList
	list     *picker
	input    *textinput.Model
	action   string
	status   string
	failed   bool
	dirty    bool
	closing  bool
}

func newKeybindingsEditor(cfg *config.Config) *keybindingsEditor {
	bindings, err := config.KeybindingsAt(cfg.UserConfigPath())
	e := &keybindingsEditor{bindings: maps.Clone(bindings)}
	if err != nil {
		e.bindings = map[string]config.KeyList{}
		e.status, e.failed = err.Error(), true
	}
	e.rebuild()
	return e
}

func (e *keybindingsEditor) rebuild() {
	km, _ := newKeymap(e.bindings)
	p := &picker{}
	for _, d := range defaultKeys {
		detail := km.all(d.action)
		note := "default"
		if keys, ok := e.bindings[d.action]; ok {
			note = "custom"
			if len(keys) == 0 {
				note = "unbound"
			}
		}
		p.items = append(p.items, pickItem{section: "actions", label: d.action, detail: detail, note: note, value: d.action})
	}
	p.home()
	e.list = p
}

func (m *model) openKeybindingsEditor() tea.Cmd {
	m.settings = &settingsPanel{keybindings: newKeybindingsEditor(m.opts.Config)}
	return nil
}

func defaultKeysFor(action string) []string {
	for _, d := range defaultKeys {
		if d.action == action {
			return append([]string(nil), d.keys...)
		}
	}
	return nil
}

func (e *keybindingsEditor) startEdit(action string, width int) tea.Cmd {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.Placeholder = "comma-separated keys · empty unbinds · default restores"
	ti.SetWidth(max(width-10, 20))
	if keys, ok := e.bindings[action]; ok {
		ti.SetValue(strings.Join(keys, ", "))
	} else {
		ti.SetValue(strings.Join(defaultKeysFor(action), ", "))
	}
	e.action, e.input, e.status = action, &ti, ""
	return ti.Focus()
}

func (e *keybindingsEditor) finishEdit() {
	raw := strings.TrimSpace(e.input.Value())
	draft := maps.Clone(e.bindings)
	if strings.EqualFold(raw, "default") {
		delete(draft, e.action)
	} else {
		var keys config.KeyList
		if raw != "" {
			for _, part := range strings.Split(raw, ",") {
				key := strings.ToLower(strings.TrimSpace(part))
				if err := checkKey(e.action, key); err != nil {
					e.status, e.failed = err.Error(), true
					return
				}
				keys = append(keys, key)
			}
		}
		draft[e.action] = keys
	}
	_, warnings := newKeymap(draft)
	if len(warnings) > 0 {
		e.status, e.failed = warnings[0], true
		return
	}
	e.bindings, e.input, e.dirty, e.failed = draft, nil, true, false
	e.status = "Not saved yet"
	e.rebuild()
}

func (m *model) saveKeybindings() tea.Cmd {
	e := m.settings.keybindings
	var value any = e.bindings
	if len(e.bindings) == 0 {
		value = nil
	}
	if err := m.opts.Config.SetUserSettingPath("keybindings", value); err != nil {
		e.status, e.failed = "Couldn't save: "+err.Error(), true
		return nil
	}
	m.opts.Config.Keybindings = maps.Clone(e.bindings)
	m.keys, m.keyWarn = newKeymap(e.bindings)
	e.dirty, e.closing, e.failed = false, false, false
	e.status = savedStatus(m.opts.Config.UserConfigPath(), savedActive)
	return nil
}

func (m *model) handleKeybindingsEditorKey(msg tea.KeyPressMsg) tea.Cmd {
	e := m.settings.keybindings
	k := msg.String()
	if e.input != nil {
		switch k {
		case "esc", "ctrl+c":
			e.input, e.status = nil, ""
			return nil
		case "enter":
			e.finishEdit()
			return nil
		}
		ti, cmd := e.input.Update(msg)
		e.input = &ti
		return cmd
	}
	switch k {
	case "ctrl+c":
		m.settings = nil
		return nil
	case "esc":
		if e.dirty && !e.closing {
			e.status, e.failed, e.closing = "Unsaved changes · s save · esc discard", true, true
			return nil
		}
		m.settings = nil
		return nil
	case "s", "ctrl+s":
		return m.saveKeybindings()
	case "u":
		if it, ok := e.list.selected(); ok {
			e.bindings[it.value.(string)] = config.KeyList{}
			e.dirty, e.status, e.failed = true, "Unbound in draft · s to save", false
			e.rebuild()
		}
		return nil
	case "r":
		if it, ok := e.list.selected(); ok {
			delete(e.bindings, it.value.(string))
			e.dirty, e.status, e.failed = true, "Restored default in draft · s to save", false
			e.rebuild()
		}
		return nil
	}
	if e.list.handleKey(msg) {
		it, _ := e.list.selected()
		return e.startEdit(it.value.(string), m.width)
	}
	return nil
}

func (m *model) keybindingsEditorView() string {
	e := m.settings.keybindings
	w := max(m.width-6, 20)
	head := spread(m.st.accent.Render("Keybindings"), m.st.dim.Render("personal · all projects"), w)
	var body, hint string
	if e.input != nil {
		body = m.st.dim.Render(e.action+" · key names as Bubble Tea reports them") + "\n" + e.input.View()
		hint = "enter apply to draft · esc cancel"
	} else {
		e.list.height = max(m.availablePanelRows()-5-boolRows(e.status != ""), 1)
		body = e.list.view(m.st, w)
		hint = "↑/↓ move · enter edit · u unbind · r restore default · s save · esc close"
	}
	out := head + "\n" + body
	if e.status != "" {
		style := m.st.ok
		if e.failed {
			style = m.st.err
		}
		out += "\n" + style.Render(e.status)
	}
	return m.st.modal.Width(max(m.width-2, 10)).Render(out + "\n" + m.st.dim.Render(hint))
}
