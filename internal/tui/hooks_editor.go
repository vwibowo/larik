package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"larik/internal/config"
	"larik/internal/hooks"
	"larik/internal/providers"
)

// hooksEditor edits only personal hooks. The maps keep opaque fields on existing entries.
type hooksEditor struct {
	events          map[string][]any
	cfg             *config.Config
	changed         map[string][]any
	event           string
	matcher, hook   int // -1 selects the parent level
	list            *picker
	input           *textinput.Model
	editing, status string
	failed, closing bool
	readFailed      error
}

func newHooksEditor(c *config.Config) *hooksEditor {
	e := &hooksEditor{cfg: c, changed: map[string][]any{}, matcher: -1, hook: -1}
	e.events, e.readFailed = config.PersonalHooksAt(c.UserConfigPath())
	if e.readFailed != nil {
		e.events = map[string][]any{}
		e.status, e.failed = e.readFailed.Error(), true
	}
	e.rebuild()
	return e
}

func (e *hooksEditor) matchers() []any            { return e.events[e.event] }
func (e *hooksEditor) matcherMap() map[string]any { return e.matchers()[e.matcher].(map[string]any) }
func (e *hooksEditor) hookMap() map[string]any {
	return e.matcherMap()["hooks"].([]any)[e.hook].(map[string]any)
}
func hookString(m map[string]any, key string) string { v, _ := m[key].(string); return v }
func (e *hooksEditor) dirty() {
	e.changed[e.event] = e.events[e.event]
	e.status, e.failed, e.closing = "Not saved yet", false, false
	e.rebuild()
}

func (e *hooksEditor) rebuild() {
	p := &picker{}
	switch {
	case e.event == "":
		for _, ev := range hooks.Events {
			name := string(ev)
			p.items = append(p.items, pickItem{section: "events", label: name, detail: fmt.Sprintf("%d personal matchers", len(e.events[name])), value: name})
		}
	case e.matcher < 0:
		for i, v := range e.matchers() {
			m := v.(map[string]any)
			label := hookString(m, "matcher")
			if label == "" {
				label = "* (all)"
			}
			p.items = append(p.items, pickItem{section: e.event, label: label, detail: fmt.Sprintf("%d hooks", len(m["hooks"].([]any))), value: fmt.Sprintf("m:%d", i)})
		}
		p.items = append(p.items, pickItem{section: "actions", label: "Add matcher", value: "add"}, pickItem{section: "actions", label: "Back to events", value: "back"})
	case e.hook < 0:
		m := e.matcherMap()
		label := hookString(m, "matcher")
		if label == "" {
			label = "* (all)"
		}
		p.items = append(p.items, pickItem{section: "matcher", label: "Pattern (regex; * matches all)", detail: label, value: "pattern"})
		for i, v := range m["hooks"].([]any) {
			h := v.(map[string]any)
			typ := hookString(h, "type")
			if typ == "" {
				typ = "command"
			}
			detail := hookString(h, typ)
			p.items = append(p.items, pickItem{section: "hooks", label: typ, detail: detail, value: fmt.Sprintf("h:%d", i)})
		}
		p.items = append(p.items, pickItem{section: "actions", label: "Add command hook", value: "command"}, pickItem{section: "actions", label: "Add prompt hook", value: "prompt"}, pickItem{section: "actions", label: "Delete matcher", value: "delete"}, pickItem{section: "actions", label: "Back to matchers", value: "back"})
	default:
		h := e.hookMap()
		typ := hookString(h, "type")
		if typ == "" {
			typ = "command"
		}
		p.items = append(p.items, pickItem{section: "hook", label: "Type", detail: typ, value: "type"})
		field := "command"
		if typ == "prompt" {
			field = "prompt"
		}
		p.items = append(p.items, pickItem{section: "hook", label: strings.ToUpper(field[:1]) + field[1:], detail: hookString(h, field), value: field})
		if typ == "prompt" {
			p.items = append(p.items, pickItem{section: "hook", label: "Model (blank uses explore/session)", detail: hookString(h, "model"), value: "model"})
		}
		timeout := "default"
		if v, ok := h["timeout"].(float64); ok {
			timeout = strconv.Itoa(int(v))
		}
		if v, ok := h["timeout"].(int); ok {
			timeout = strconv.Itoa(v)
		}
		p.items = append(p.items, pickItem{section: "hook", label: "Timeout (seconds)", detail: timeout, value: "timeout"}, pickItem{section: "actions", label: "Delete hook", value: "delete"}, pickItem{section: "actions", label: "Back to matcher", value: "back"})
	}
	p.home()
	e.list = p
}

func (m *model) openHooksEditor() tea.Cmd {
	m.settings = &settingsPanel{hooks: newHooksEditor(m.opts.Config)}
	return nil
}
func (e *hooksEditor) startInput(field string, width int) tea.Cmd {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.SetWidth(max(width-10, 20))
	e.editing = field
	if field == "pattern" {
		ti.SetValue(hookString(e.matcherMap(), "matcher"))
	} else {
		h := e.hookMap()
		if field == "timeout" {
			if v, ok := h[field].(float64); ok {
				ti.SetValue(strconv.Itoa(int(v)))
			}
			if v, ok := h[field].(int); ok {
				ti.SetValue(strconv.Itoa(v))
			}
		} else {
			ti.SetValue(hookString(h, field))
		}
	}
	e.input = &ti
	e.status = ""
	return ti.Focus()
}
func (e *hooksEditor) finishInput() {
	value := strings.TrimSpace(e.input.Value())
	field := e.editing
	switch field {
	case "pattern":
		if value != "" && value != "*" {
			if _, err := regexp.Compile("(?i)^(?:" + value + ")$"); err != nil {
				e.status, e.failed = "Invalid matcher regex: "+err.Error(), true
				return
			}
		}
		if value == "" {
			delete(e.matcherMap(), "matcher")
		} else {
			e.matcherMap()["matcher"] = value
		}
	case "timeout":
		if value == "" {
			delete(e.hookMap(), field)
		} else {
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 86400 {
				e.status, e.failed = "Timeout must be 1–86400 seconds (or blank for default)", true
				return
			}
			e.hookMap()[field] = n
		}
	default:
		if value == "" {
			delete(e.hookMap(), field)
		} else {
			e.hookMap()[field] = value
		}
	}
	e.input = nil
	e.dirty()
}
func (e *hooksEditor) validate() error {
	for _, ev := range hooks.Events {
		if _, changed := e.changed[string(ev)]; !changed {
			continue
		}
		for i, v := range e.events[string(ev)] {
			m := v.(map[string]any)
			pattern := hookString(m, "matcher")
			if pattern != "" && pattern != "*" {
				if _, err := regexp.Compile("(?i)^(?:" + pattern + ")$"); err != nil {
					return fmt.Errorf("%s matcher %d: %w", ev, i+1, err)
				}
			}
			for j, v := range m["hooks"].([]any) {
				h := v.(map[string]any)
				typ := hookString(h, "type")
				if typ == "" {
					typ = "command"
				}
				if typ != "command" && typ != "prompt" {
					return fmt.Errorf("%s hook %d: unsupported type %q", ev, j+1, typ)
				}
				if hookString(h, typ) == "" {
					return fmt.Errorf("%s hook %d needs a %s", ev, j+1, typ)
				}
				if typ == "command" && hookString(h, "prompt") != "" || typ == "prompt" && hookString(h, "command") != "" {
					return fmt.Errorf("%s hook %d has fields for both types", ev, j+1)
				}
				if typ == "prompt" {
					model := hookString(h, "model")
					if model != "" && !providers.IsRole(e.cfg, model) {
						provider, name, ok := strings.Cut(model, "/")
						if !ok || provider == "" || name == "" || strings.ContainsAny(model, " \t\n") {
							return fmt.Errorf("%s hook %d: model must be a routing role or provider/model", ev, j+1)
						}
					}
				}
				if v, ok := h["timeout"]; ok {
					n, ok := v.(int)
					if !ok {
						f, yes := v.(float64)
						n, ok = int(f), yes && f == float64(int(f))
					}
					if !ok || n < 1 || n > 86400 {
						return fmt.Errorf("%s hook %d: timeout must be 1–86400 seconds", ev, j+1)
					}
				}
			}
		}
	}
	return nil
}
func (m *model) saveHooksEditor() tea.Cmd {
	e := m.settings.hooks
	if e.readFailed != nil {
		e.status, e.failed = "Cannot save: personal config could not be read", true
		return nil
	}
	if len(e.changed) == 0 {
		e.status = "No changes to save"
		return nil
	}
	if err := e.validate(); err != nil {
		e.status, e.failed = err.Error(), true
		return nil
	}
	if err := m.opts.Config.PatchUserHooks(e.changed); err != nil {
		e.status, e.failed = "Couldn't save: "+err.Error(), true
		return nil
	}
	e.changed = map[string][]any{}
	e.failed, e.closing = false, false
	if m.canReloadApp() {
		m.settings = nil
		return tea.Batch(m.reloadApp(), m.println(m.st.dim.Render("Saved personal hooks to "+shortHome(m.opts.Config.UserConfigPath()))))
	}
	e.status = "Saved personal hooks · active after /reload (fresh context)"
	return nil
}
func (m *model) requestHooksSave() tea.Cmd {
	if len(m.settings.hooks.changed) > 0 && m.hasContext() && m.canReloadApp() {
		return m.askReload("Personal hooks run commands or send prompts to a model. Save and reload services with a fresh model context? (Transcript stays saved)", m.saveHooksEditor)
	}
	return m.saveHooksEditor()
}
func (m *model) handleHooksKey(msg tea.KeyPressMsg) tea.Cmd {
	e := m.settings.hooks
	k := msg.String()
	if e.input != nil {
		switch k {
		case "esc", "ctrl+c":
			e.input = nil
			return nil
		case "enter":
			e.finishInput()
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
		if e.hook >= 0 {
			e.hook = -1
		} else if e.matcher >= 0 {
			e.matcher = -1
		} else if e.event != "" {
			e.event = ""
		} else if len(e.changed) > 0 && !e.closing {
			e.status, e.failed, e.closing = "Unsaved changes · s save · esc discard", true, true
			return nil
		} else {
			m.settings = nil
			return nil
		}
		e.rebuild()
		return nil
	case "s", "ctrl+s":
		return m.requestHooksSave()
	}
	if !e.list.handleKey(msg) {
		return nil
	}
	item, _ := e.list.selected()
	v := item.value.(string)
	switch {
	case e.event == "":
		e.event = v
		e.rebuild()
	case e.matcher < 0:
		if v == "back" {
			e.event = ""
		} else if v == "add" {
			e.events[e.event] = append(e.matchers(), map[string]any{"hooks": []any{}})
			e.matcher = len(e.matchers()) - 1
			e.dirty()
		} else {
			fmt.Sscanf(v, "m:%d", &e.matcher)
		}
		e.rebuild()
	case e.hook < 0:
		switch v {
		case "back":
			e.matcher = -1
		case "delete":
			ms := e.matchers()
			e.events[e.event] = append(ms[:e.matcher], ms[e.matcher+1:]...)
			e.matcher = -1
			e.dirty()
		case "pattern":
			return e.startInput(v, m.width)
		case "command", "prompt":
			h := map[string]any{"type": v}
			hs := e.matcherMap()["hooks"].([]any)
			e.matcherMap()["hooks"] = append(hs, h)
			e.hook = len(hs)
			e.dirty()
		default:
			fmt.Sscanf(v, "h:%d", &e.hook)
		}
		e.rebuild()
	default:
		switch v {
		case "back":
			e.hook = -1
		case "delete":
			hs := e.matcherMap()["hooks"].([]any)
			e.matcherMap()["hooks"] = append(hs[:e.hook], hs[e.hook+1:]...)
			e.hook = -1
			e.dirty()
		case "type":
			h := e.hookMap()
			if hookString(h, "type") == "prompt" {
				h["type"] = "command"
				delete(h, "prompt")
				delete(h, "model")
			} else {
				h["type"] = "prompt"
				delete(h, "command")
			}
			e.dirty()
		default:
			return e.startInput(v, m.width)
		}
		e.rebuild()
	}
	return nil
}
func (m *model) hooksEditorView() string {
	e := m.settings.hooks
	w := max(m.width-6, 20)
	head := spread(m.st.accent.Render("Personal lifecycle hooks"), m.st.dim.Render("trusted · all projects"), w)
	body, hint := "", "↑/↓ move · enter select/change · s save · esc back/close"
	if e.input != nil {
		body = m.st.dim.Render(e.editing) + "\n" + e.input.View()
		hint = "enter apply to draft · esc cancel"
	} else {
		e.list.height = max(m.availablePanelRows()-10-boolRows(e.status != ""), 1)
		body = e.list.view(m.st, w)
	}
	out := head + "\n" + body + "\n" + m.st.err.Render("Warning: command hooks execute shell code; prompt hooks send input to a model. Only add hooks you trust.") + "\n" + m.st.dim.Render("Shared project hooks and their approval are not edited here. /hooks shows status.")
	if e.status != "" {
		style := m.st.ok
		if e.failed {
			style = m.st.err
		}
		out += "\n" + style.Render(e.status)
	}
	return m.st.modal.Width(max(m.width-2, 10)).Render(out + "\n" + m.st.dim.Render(hint))
}
