package tui

import (
	"net/url"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"larik/internal/config"
)

// endpointEditor edits one personal web search or audio endpoint. Only touched
// leaves are written: merged project values and unknown siblings never leak
// into the user's configuration.
type endpointEditor struct {
	kind                        string // web, stt, tts
	fields                      map[string]string
	changes                     map[string]any
	list                        *picker
	input                       *textinput.Model
	editing                     string
	status                      string
	failed, closing, readFailed bool
}

func newEndpointEditor(cfg *config.Config, kind string) *endpointEditor {
	e := &endpointEditor{kind: kind, fields: map[string]string{}, changes: map[string]any{}}
	if kind == "web" {
		v, err := config.WebSearchAt(cfg.UserConfigPath())
		if err != nil {
			e.status, e.failed, e.readFailed = err.Error(), true, true
		} else {
			e.fields["provider"], e.fields["url"], e.fields["api_key_env"], e.fields["api_key"] = v.Provider, v.URL, v.APIKeyEnv, v.APIKey
			if v.Disabled {
				e.fields["disabled"] = "on"
			}
		}
	} else {
		v, err := config.AudioEndpointAt(cfg.UserConfigPath(), kind)
		if err != nil {
			e.status, e.failed, e.readFailed = err.Error(), true, true
		} else {
			e.fields["base_url"], e.fields["model"], e.fields["api_key_env"], e.fields["api_key"] = v.BaseURL, v.Model, v.APIKeyEnv, v.APIKey
			e.fields["language"], e.fields["voice"] = v.Language, v.Voice
		}
	}
	e.rebuild()
	return e
}

// title names the endpoint in the editor's own header and in what it reports
// after saving, so both say the same thing.
func (e *endpointEditor) title() string {
	switch e.kind {
	case "stt":
		return "Speech-to-text endpoint"
	case "tts":
		return "Text-to-speech endpoint"
	}
	return "Web search"
}

func (e *endpointEditor) prefix() string {
	if e.kind == "web" {
		return "web.search."
	}
	return "audio." + e.kind + "."
}

func (e *endpointEditor) change(field string, value any) {
	if s, ok := value.(string); ok {
		e.fields[field] = s
	} else if value == true {
		e.fields[field] = "on"
	} else {
		e.fields[field] = "off"
	}
	e.changes[e.prefix()+field] = value
	e.status, e.failed, e.closing = "Not saved yet", false, false
	e.rebuild()
}

func (e *endpointEditor) rebuild() {
	p := &picker{}
	add := func(field, label string) {
		detail := e.fields[field]
		if field == "api_key" {
			if detail != "" {
				detail = "•••• · enter replaces · d clears"
			} else {
				detail = "not set · enter to set"
			}
		} else if detail == "" {
			detail = "not set"
		}
		p.items = append(p.items, pickItem{section: "configuration", label: label, detail: detail, value: field})
	}
	if e.kind == "web" {
		add("disabled", "Disabled")
		add("provider", "Provider")
		add("url", "URL")
	} else {
		add("base_url", "Base URL")
		add("model", "Model")
		add("language", "Language")
		add("voice", "Voice")
	}
	add("api_key_env", "API key environment variable")
	add("api_key", "API key")
	if e.fields["api_key"] != "" {
		p.items = append(p.items, pickItem{section: "actions", label: "Clear API key", value: "clear_key"})
	}
	p.home()
	e.list = p
}

func (m *model) openWebSearchEditor() tea.Cmd {
	m.settings = &settingsPanel{endpoint: newEndpointEditor(m.opts.Config, "web")}
	return nil
}
func (m *model) openSTTEditor() tea.Cmd {
	m.settings = &settingsPanel{endpoint: newEndpointEditor(m.opts.Config, "stt")}
	return nil
}
func (m *model) openTTSEditor() tea.Cmd {
	m.settings = &settingsPanel{endpoint: newEndpointEditor(m.opts.Config, "tts")}
	return nil
}

func (e *endpointEditor) startInput(field string, width int) tea.Cmd {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.SetWidth(max(width-10, 20))
	ti.Placeholder = "empty to clear"
	if field == "api_key" {
		ti.EchoMode = textinput.EchoPassword
		ti.Placeholder = "new API key (existing key is never shown)"
	} else {
		ti.SetValue(e.fields[field])
	}
	e.editing, e.input, e.status = field, &ti, ""
	return ti.Focus()
}

// clearKey erases the stored credential. Replacing one is enter and clearing
// it is this, so an empty input can never erase a secret you cannot see.
func (e *endpointEditor) clearKey() {
	e.change("api_key", "")
	e.status, e.failed = "Cleared · not saved yet", false
}

func (e *endpointEditor) finishInput() {
	value := strings.TrimSpace(e.input.Value())
	if e.editing == "url" || e.editing == "base_url" {
		if value != "" {
			u, err := url.Parse(value)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				e.status, e.failed = "Enter an http(s) URL or leave it empty", true
				return
			}
		}
	}
	e.input = nil
	if e.editing == "api_key" && value == "" {
		// Leaving the field empty is how you back out of replacing the key, so
		// it must not erase the one already there; d and the action row do that.
		if e.fields["api_key"] != "" {
			e.status, e.failed = "Unchanged · d clears the API key", false
		}
		return
	}
	e.change(e.editing, value)
}

func (m *model) saveEndpointEditor() tea.Cmd {
	e := m.settings.endpoint
	if e.readFailed {
		e.status, e.failed = "Cannot save: personal config could not be read", true
		return nil
	}
	if len(e.changes) == 0 {
		e.status = "No changes to save"
		return nil
	}
	if err := m.opts.Config.SetUserSettingPaths(e.changes); err != nil {
		e.status, e.failed = "Couldn't save: "+err.Error(), true
		return nil
	}
	e.changes, e.closing, e.failed = map[string]any{}, false, false
	path := m.opts.Config.UserConfigPath()
	if m.canReloadApp() {
		m.settings = nil
		return tea.Batch(m.reloadApp(), m.println(m.st.dim.Render(savedAndReloaded(e.title(), path))))
	}
	e.status = savedStatus(path, savedAfterReload)
	return nil
}

func (m *model) requestEndpointSave() tea.Cmd {
	if m.settings.endpoint.readFailed || len(m.settings.endpoint.changes) == 0 {
		return m.saveEndpointEditor()
	}
	if m.hasContext() && m.canReloadApp() {
		return m.askReload("Endpoint changes need rebuilt services and a fresh model context. Apply them? (Transcript stays saved)", m.saveEndpointEditor)
	}
	return m.saveEndpointEditor()
}

func (m *model) handleEndpointKey(msg tea.KeyPressMsg) tea.Cmd {
	e := m.settings.endpoint
	k := msg.String()
	if e.input != nil {
		switch k {
		case "esc", "ctrl+c":
			e.input, e.status = nil, ""
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
		if len(e.changes) > 0 && !e.closing {
			e.status, e.failed, e.closing = "Unsaved changes · s save · esc discard", true, true
			return nil
		}
		m.settings = nil
		return nil
	case "s", "ctrl+s":
		return m.requestEndpointSave()
	case "d", "delete":
		// Clearing the key is also an action row, which is how you find it;
		// this is the shortcut, and the same key the MCP editor uses.
		if it, ok := e.list.selected(); ok && it.value == "api_key" && e.fields["api_key"] != "" {
			e.clearKey()
			return nil
		}
	}
	if !e.list.handleKey(msg) {
		return nil
	}
	it, _ := e.list.selected()
	field := it.value.(string)
	switch field {
	case "clear_key":
		e.clearKey()
		return nil
	case "disabled":
		e.change("disabled", e.fields["disabled"] != "on")
		return nil
	case "provider":
		choices := []string{"", "brave", "tavily", "searxng", "ddg"}
		for i, v := range choices {
			if v == e.fields["provider"] {
				e.change("provider", choices[(i+1)%len(choices)])
				return nil
			}
		}
		e.change("provider", "brave")
		return nil
	default:
		return e.startInput(field, m.width)
	}
}

func (m *model) endpointEditorView() string {
	e := m.settings.endpoint
	w := max(m.width-6, 20)
	head := spread(m.st.accent.Render(e.title()), m.st.dim.Render("personal · all projects"), w)
	var body, hint string
	if e.input != nil {
		body = m.st.dim.Render(e.editing) + "\n" + e.input.View()
		hint = "enter apply to draft · esc cancel"
	} else {
		e.list.height = max(m.availablePanelRows()-5-boolRows(e.status != ""), 1)
		body = e.list.view(m.st, w)
		hint = "↑/↓ move · enter change/replace · s save · esc close"
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
