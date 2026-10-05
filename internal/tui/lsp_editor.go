package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"larik/internal/config"
	"larik/internal/lsp"
)

// lspEditor keeps personal drafts separate from merged project configuration.
// Only changed fields are patched on disk; opaque initialization options stay untouched.
type lspEditor struct {
	servers         map[string]lsp.ServerConfig
	changes         map[string]map[string]any // nil entry removes a personal server
	name            string
	list            *picker
	input           *textinput.Model
	editing         string
	status          string
	failed, closing bool
	readFailed      error
}

func newLSPEditor(cfg *config.Config) *lspEditor {
	e := &lspEditor{changes: map[string]map[string]any{}}
	e.servers, e.readFailed = config.LSPAt(cfg.UserConfigPath())
	if e.readFailed != nil {
		e.status, e.failed = e.readFailed.Error(), true
		e.servers = map[string]lsp.ServerConfig{}
	}
	e.rebuild()
	return e
}

func (e *lspEditor) rebuild() {
	p := &picker{}
	if e.name == "" {
		names := map[string]bool{}
		for n := range lsp.Builtins {
			names[n] = true
		}
		for n := range e.servers {
			names[n] = true
		}
		sorted := make([]string, 0, len(names))
		for n := range names {
			sorted = append(sorted, n)
		}
		sort.Strings(sorted)
		for _, n := range sorted {
			_, builtin := lsp.Builtins[n]
			section, detail := "personal", "custom server"
			if builtin {
				section, detail = "built-in", "default"
				if _, ok := e.servers[n]; ok {
					detail = "personal override"
				}
			}
			if e.servers[n].Disabled {
				detail += " · disabled"
			}
			p.items = append(p.items, pickItem{section: section, label: n, detail: detail, value: n})
		}
		p.items = append(p.items, pickItem{section: "actions", label: "Add personal server", value: "+"})
	} else {
		c := e.servers[e.name]
		_, builtin := lsp.Builtins[e.name]
		p.items = append(p.items, pickItem{section: e.name, label: "Disabled", detail: onOff(c.Disabled), value: "disabled"})
		if !builtin {
			for _, f := range []struct {
				key, label string
				value      []string
			}{
				{"command", "Command argv (comma-separated arguments)", c.Command},
				{"extensions", "Extensions (comma-separated)", c.Extensions},
				{"root_markers", "Root markers (comma-separated)", c.RootMarkers},
			} {
				p.items = append(p.items, pickItem{section: e.name, label: f.label, detail: strings.Join(f.value, ", "), value: f.key})
			}
			p.items = append(p.items, pickItem{section: e.name, label: "Language ID", detail: c.LanguageID, value: "language_id"})
			keys := make([]string, 0, len(c.Env))
			for k := range c.Env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				p.items = append(p.items, pickItem{section: "environment", label: k, detail: c.Env[k], value: "env:" + k})
			}
			p.items = append(p.items, pickItem{section: "actions", label: "Add environment variable", value: "env+"}, pickItem{section: "actions", label: "Delete personal server", value: "delete"})
		}
		p.items = append(p.items, pickItem{section: "actions", label: "Back to servers", value: "back"})
	}
	p.home()
	e.list = p
}

func (e *lspEditor) change(field string, value any) {
	c := e.servers[e.name]
	switch field {
	case "disabled":
		c.Disabled = value.(bool)
	case "command":
		c.Command = value.([]string)
	case "extensions":
		c.Extensions = value.([]string)
	case "root_markers":
		c.RootMarkers = value.([]string)
	case "language_id":
		c.LanguageID = value.(string)
	default:
		if c.Env == nil {
			c.Env = map[string]string{}
		}
		key := strings.TrimPrefix(field, "env:")
		if value == nil {
			delete(c.Env, key)
		} else {
			c.Env[key] = value.(string)
		}
	}
	e.servers[e.name] = c
	if e.changes[e.name] == nil {
		e.changes[e.name] = map[string]any{}
	}
	e.changes[e.name][field] = value
	e.status, e.failed, e.closing = "Not saved yet", false, false
	e.rebuild()
}

func (m *model) openLSPEditor() tea.Cmd {
	m.settings = &settingsPanel{lsp: newLSPEditor(m.opts.Config)}
	return nil
}

func (e *lspEditor) startInput(field string, width int) tea.Cmd {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.SetWidth(max(width-10, 20))
	e.editing = field
	c := e.servers[e.name]
	switch field {
	case "command":
		ti.SetValue(strings.Join(c.Command, "\n"))
	case "extensions":
		ti.SetValue(strings.Join(c.Extensions, "\n"))
	case "root_markers":
		ti.SetValue(strings.Join(c.RootMarkers, "\n"))
	case "language_id":
		ti.SetValue(c.LanguageID)
	default:
		if strings.HasPrefix(field, "env:") {
			ti.SetValue(c.Env[strings.TrimPrefix(field, "env:")])
		}
	}
	if field == "name" {
		ti.Placeholder = "unique server name"
	}
	if field == "env+" {
		ti.Placeholder = "environment variable name"
	}
	e.input = &ti
	e.status = ""
	return ti.Focus()
}

// Commas separate argv elements; spaces inside each element are preserved.
// Neither the input nor the resulting command is interpreted by a shell.
func lspWords(s string) []string {
	var result []string
	for _, line := range strings.Split(s, "\n") {
		for _, part := range strings.Split(line, ",") {
			if v := strings.TrimSpace(part); v != "" {
				result = append(result, v)
			}
		}
	}
	return result
}

func (e *lspEditor) finishInput() {
	v := strings.TrimSpace(e.input.Value())
	field := e.editing
	if field == "name" {
		if v == "" || strings.ContainsAny(v, ". \t\n") {
			e.status, e.failed = "Enter a nonempty name without dots or spaces", true
			return
		}
		if _, ok := e.servers[v]; ok {
			e.status, e.failed = "Server already exists", true
			return
		}
		if _, ok := lsp.Builtins[v]; ok {
			e.status, e.failed = "Built-in server already exists", true
			return
		}
		e.name = v
		e.servers[v] = lsp.ServerConfig{}
		e.changes[v] = map[string]any{}
		e.input = nil
		e.status = "Not saved yet"
		e.rebuild()
		return
	}
	if field == "env+" {
		if v == "" || strings.ContainsAny(v, " =.\t\n") {
			e.status, e.failed = "Enter one environment variable name", true
			return
		}
		e.startInput("env:"+v, max(e.input.Width()+10, 30))
		e.input.Placeholder = "value (empty to remove)"
		return
	}
	e.input = nil
	switch field {
	case "command", "extensions", "root_markers":
		e.change(field, lspWords(v))
	case "language_id":
		e.change(field, v)
	default:
		if strings.HasPrefix(field, "env:") {
			if v == "" {
				e.change(field, nil)
			} else {
				e.change(field, v)
			}
		}
	}
}

func (m *model) saveLSPEditor() tea.Cmd {
	e := m.settings.lsp
	if e.readFailed != nil {
		e.status, e.failed = "Cannot save: personal config could not be read", true
		return nil
	}
	if len(e.changes) == 0 {
		e.status = "No changes to save"
		return nil
	}
	for name, patch := range e.changes {
		if patch != nil {
			c := e.servers[name]
			if _, builtin := lsp.Builtins[name]; !builtin && (len(c.Command) == 0 || len(c.Extensions) == 0) {
				e.status, e.failed = fmt.Sprintf("%s needs command argv and extensions", name), true
				return nil
			}
		}
	}
	if err := m.opts.Config.PatchUserLSP(e.changes); err != nil {
		e.status, e.failed = "Couldn't save: "+err.Error(), true
		return nil
	}
	e.changes, e.failed, e.closing = map[string]map[string]any{}, false, false
	if m.canReloadApp() {
		m.settings = nil
		return tea.Batch(m.reloadApp(), m.println(m.st.dim.Render("Saved to "+shortHome(m.opts.Config.UserConfigPath()))))
	}
	e.status = "Saved to " + shortHome(m.opts.Config.UserConfigPath()) + " · active after /reload"
	return nil
}

func (m *model) requestLSPSave() tea.Cmd {
	if len(m.settings.lsp.changes) > 0 && m.hasContext() && m.canReloadApp() {
		return m.askReload("Language server changes need rebuilt services and a fresh model context. Apply them? (Transcript stays saved)", m.saveLSPEditor)
	}
	return m.saveLSPEditor()
}

func (m *model) handleLSPKey(msg tea.KeyPressMsg) tea.Cmd {
	e := m.settings.lsp
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
		if e.name != "" {
			e.name = ""
			e.rebuild()
			return nil
		}
		if len(e.changes) > 0 && !e.closing {
			e.status, e.failed, e.closing = "Unsaved changes · s save · esc discard", true, true
			return nil
		}
		m.settings = nil
		return nil
	case "s", "ctrl+s":
		return m.requestLSPSave()
	}
	if !e.list.handleKey(msg) {
		return nil
	}
	item, _ := e.list.selected()
	field := item.value.(string)
	if e.name == "" {
		if field == "+" {
			return e.startInput("name", m.width)
		}
		e.name = field
		e.rebuild()
		return nil
	}
	switch field {
	case "back":
		e.name = ""
		e.rebuild()
	case "delete":
		delete(e.servers, e.name)
		e.changes[e.name] = nil
		e.name = ""
		e.status = "Not saved yet"
		e.rebuild()
	case "disabled":
		e.change(field, !e.servers[e.name].Disabled)
	default:
		return e.startInput(field, m.width)
	}
	return nil
}

func (m *model) lspEditorView() string {
	e := m.settings.lsp
	w := max(m.width-6, 20)
	head := spread(m.st.accent.Render("Language servers"), m.st.dim.Render("personal · all projects"), w)
	var body, hint string
	if e.input != nil {
		body = m.st.dim.Render(e.editing) + "\n" + e.input.View()
		hint = "enter apply to draft · esc cancel"
	} else {
		e.list.height = max(m.availablePanelRows()-8-boolRows(e.status != ""), 1)
		body = e.list.view(m.st, w)
		hint = "↑/↓ move · enter select/change · s save · esc back/close"
	}
	out := head + "\n" + body + "\n" + m.st.dim.Render("initialization_options: edit only in "+shortHome(m.opts.Config.UserConfigPath())+" (preserved here)")
	if e.status != "" {
		style := m.st.ok
		if e.failed {
			style = m.st.err
		}
		out += "\n" + style.Render(e.status)
	}
	return m.st.modal.Width(max(m.width-2, 10)).Render(out + "\n" + m.st.dim.Render(hint))
}
