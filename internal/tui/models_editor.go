package tui

import (
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"larik/internal/config"
	"larik/internal/llm"
)

type modelsEditor struct {
	entries         map[string]llm.ModelInfo
	changes         map[string]*llm.ModelInfo
	name            string
	list            *picker
	input           *textinput.Model
	editing, status string
	failed, closing bool
	readFailed      error
}

var modelFields = []struct{ key, label string }{
	{"provider", "Provider"}, {"context_window", "Context window (tokens)"}, {"max_output", "Max output (tokens)"},
	{"input_price", "Input price (USD / million tokens)"}, {"output_price", "Output price (USD / million tokens)"},
	{"cache_read_price", "Cache read price (USD / million tokens)"}, {"cache_write_price", "Cache write price (USD / million tokens)"},
}

func newModelsEditor(cfg *config.Config) *modelsEditor {
	e := &modelsEditor{changes: map[string]*llm.ModelInfo{}}
	e.entries, e.readFailed = config.UserModelsAt(cfg.UserConfigPath())
	if e.readFailed != nil {
		e.entries = map[string]llm.ModelInfo{}
		e.status, e.failed = e.readFailed.Error(), true
	}
	e.rebuild()
	return e
}

func (e *modelsEditor) rebuild() {
	p := &picker{}
	if e.name == "" {
		names := make([]string, 0, len(e.entries))
		for name := range e.entries {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			p.items = append(p.items, pickItem{section: "personal", label: name, detail: e.entries[name].Provider, value: name})
		}
		p.items = append(p.items, pickItem{section: "actions", label: "Add model override", value: "+"})
	} else {
		info := e.entries[e.name]
		p.items = append(p.items, pickItem{section: e.name, label: "ID (entry name)", detail: e.name, value: "id"})
		for _, f := range modelFields {
			var value string
			switch f.key {
			case "provider":
				value = info.Provider
			case "context_window":
				value = strconv.Itoa(info.ContextWindow)
			case "max_output":
				value = strconv.Itoa(info.MaxOutput)
			case "input_price":
				value = strconv.FormatFloat(info.InputPrice, 'f', -1, 64)
			case "output_price":
				value = strconv.FormatFloat(info.OutputPrice, 'f', -1, 64)
			case "cache_read_price":
				value = strconv.FormatFloat(info.CacheRead, 'f', -1, 64)
			case "cache_write_price":
				value = strconv.FormatFloat(info.CacheWrite, 'f', -1, 64)
			}
			p.items = append(p.items, pickItem{section: e.name, label: f.label, detail: value, value: f.key})
		}
		p.items = append(p.items, pickItem{section: "actions", label: "Delete personal override", value: "delete"}, pickItem{section: "actions", label: "Back to models", value: "back"})
	}
	p.home()
	e.list = p
}

func (e *modelsEditor) startInput(field string, width int) tea.Cmd {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.SetWidth(max(width-10, 20))
	e.editing = field
	if field == "name" {
		ti.Placeholder = "model id or provider/model"
	} else {
		info := e.entries[e.name]
		switch field {
		case "provider":
			ti.SetValue(info.Provider)
		case "context_window":
			ti.SetValue(strconv.Itoa(info.ContextWindow))
		case "max_output":
			ti.SetValue(strconv.Itoa(info.MaxOutput))
		case "input_price":
			ti.SetValue(strconv.FormatFloat(info.InputPrice, 'f', -1, 64))
		case "output_price":
			ti.SetValue(strconv.FormatFloat(info.OutputPrice, 'f', -1, 64))
		case "cache_read_price":
			ti.SetValue(strconv.FormatFloat(info.CacheRead, 'f', -1, 64))
		case "cache_write_price":
			ti.SetValue(strconv.FormatFloat(info.CacheWrite, 'f', -1, 64))
		}
	}
	e.input = &ti
	e.status = ""
	return ti.Focus()
}

func (e *modelsEditor) finishInput() {
	value := strings.TrimSpace(e.input.Value())
	field := e.editing
	if field == "name" {
		if value == "" || strings.ContainsAny(value, " \t\r\n") {
			e.status, e.failed = "Enter a nonempty model ID without spaces", true
			return
		}
		if _, ok := e.entries[value]; ok {
			e.status, e.failed = "Personal override already exists", true
			return
		}
		e.name = value
		e.entries[value] = llm.ModelInfo{ID: value}
		e.draft()
		e.input = nil
		e.rebuild()
		return
	}
	info := e.entries[e.name]
	switch field {
	case "provider":
		info.Provider = value
	case "context_window", "max_output":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			e.status, e.failed = "Enter a nonnegative whole number", true
			return
		}
		if field == "context_window" {
			info.ContextWindow = n
		} else {
			info.MaxOutput = n
		}
	default:
		n, err := strconv.ParseFloat(value, 64)
		if err != nil || n < 0 || n != n || n > 1e308 {
			e.status, e.failed = "Enter a finite nonnegative price", true
			return
		}
		switch field {
		case "input_price":
			info.InputPrice = n
		case "output_price":
			info.OutputPrice = n
		case "cache_read_price":
			info.CacheRead = n
		case "cache_write_price":
			info.CacheWrite = n
		}
	}
	e.entries[e.name] = info
	e.draft()
	e.input = nil
	e.rebuild()
}

func (e *modelsEditor) draft() {
	info := e.entries[e.name]
	e.changes[e.name] = &info
	e.status, e.failed, e.closing = "Not saved yet", false, false
}

func (m *model) openModelsEditor() tea.Cmd {
	m.settings = &settingsPanel{models: newModelsEditor(m.opts.Config)}
	return nil
}
func (m *model) saveModelsEditor() tea.Cmd {
	e := m.settings.models
	if e.readFailed != nil {
		e.status, e.failed = "Cannot save: personal config could not be read", true
		return nil
	}
	if len(e.changes) == 0 {
		e.status = "No changes to save"
		return nil
	}
	if err := m.opts.Config.PatchUserModels(e.changes); err != nil {
		e.status, e.failed = "Couldn't save: "+err.Error(), true
		return nil
	}
	if m.canReloadApp() {
		for id, info := range e.changes {
			if info == nil {
				llm.RestoreCatalogEntry(id)
			}
		}
	}
	e.changes = map[string]*llm.ModelInfo{}
	e.failed, e.closing = false, false
	if m.canReloadApp() {
		m.settings = nil
		return tea.Batch(m.reloadApp(), m.println(m.st.dim.Render("Saved to "+shortHome(m.opts.Config.UserConfigPath()))))
	}
	e.status = "Saved to " + shortHome(m.opts.Config.UserConfigPath()) + " · active after /reload"
	return nil
}
func (m *model) requestModelsSave() tea.Cmd {
	if len(m.settings.models.changes) > 0 && m.hasContext() && m.canReloadApp() {
		return m.askReload("Model catalog changes need a fresh context. Apply them? (Transcript stays saved)", m.saveModelsEditor)
	}
	return m.saveModelsEditor()
}
func (m *model) handleModelsKey(msg tea.KeyPressMsg) tea.Cmd {
	e := m.settings.models
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
		return m.requestModelsSave()
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
		delete(e.entries, e.name)
		e.changes[e.name] = nil
		e.name = ""
		e.status = "Not saved yet"
		e.rebuild()
	case "id": // The ID is the entry name; rename by adding and deleting an override.
	default:
		return e.startInput(field, m.width)
	}
	return nil
}
func (m *model) modelsEditorView() string {
	e := m.settings.models
	w := max(m.width-6, 20)
	head := spread(m.st.accent.Render("Model catalog overrides"), m.st.dim.Render("personal · all projects"), w)
	var body, hint string
	if e.input != nil {
		body = m.st.dim.Render(e.editing) + "\n" + e.input.View()
		hint = "enter apply to draft · esc cancel"
	} else {
		e.list.height = max(m.availablePanelRows()-8-boolRows(e.status != ""), 1)
		body = e.list.view(m.st, w)
		hint = "↑/↓ move · enter select/change · s save · esc back/close"
	}
	out := head + "\n" + body + "\n" + m.st.dim.Render("Unknown model fields are preserved on save")
	if e.status != "" {
		style := m.st.ok
		if e.failed {
			style = m.st.err
		}
		out += "\n" + style.Render(e.status)
	}
	return m.st.modal.Width(max(m.width-2, 10)).Render(out + "\n" + m.st.dim.Render(hint))
}
