package tui

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"larik/internal/config"
)

// mcpEditor stages edits to personal servers only. Credential values are never
// loaded into an input or displayed: changing one requires explicit replacement.
type mcpEditor struct {
	servers         map[string]config.MCPServer
	changes         map[string]map[string]any
	name            string
	list            *picker
	input           *textinput.Model
	editing, status string
	failed, closing bool
	readFailed      error
}

func newMCPEditor(cfg *config.Config) *mcpEditor {
	e := &mcpEditor{changes: map[string]map[string]any{}}
	e.servers, e.readFailed = config.MCPAt(cfg.UserConfigPath())
	if e.readFailed != nil {
		e.servers = map[string]config.MCPServer{}
		e.status, e.failed = e.readFailed.Error(), true
	}
	e.rebuild()
	return e
}
func (e *mcpEditor) rebuild() {
	p := &picker{}
	if e.name == "" {
		names := make([]string, 0, len(e.servers))
		for n := range e.servers {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			s := e.servers[n]
			detail := s.Transport()
			if s.Disabled {
				detail += " · disabled"
			}
			p.items = append(p.items, pickItem{section: "personal", label: n, detail: detail, value: n})
		}
		p.items = append(p.items, pickItem{section: "actions", label: "Add personal server", value: "+"})
	} else {
		s := e.servers[e.name]
		add := func(section, label, detail, key string) {
			p.items = append(p.items, pickItem{section: section, label: label, detail: detail, value: key})
		}
		add(e.name, "Enabled", onOff(!s.Disabled), "disabled")
		add(e.name, "Transport", s.Transport(), "type")
		if s.Transport() == "stdio" {
			add(e.name, "Command", s.Command, "command")
			add(e.name, "Arguments (comma-separated)", strings.Join(s.Args, ", "), "args")
		} else {
			add(e.name, "URL", s.URL, "url")
		}
		for _, group := range []struct {
			title, prefix string
			values        map[string]string
		}{{"environment", "env", s.Env}, {"headers", "headers", s.Headers}} {
			keys := make([]string, 0, len(group.values))
			for k := range group.values {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				add(group.title, k, "•••• (enter to replace; empty to clear)", group.prefix+":"+k)
			}
			add("actions", "Add "+group.title+" key", "", group.prefix+"+")
		}
		if s.Transport() != "stdio" {
			id, secret, scopes, port := "", "not set", "", "auto"
			if s.OAuth != nil {
				id = s.OAuth.ClientID
				scopes = strings.Join(s.OAuth.Scopes, ", ")
				if s.OAuth.ClientSecret != "" {
					secret = "•••• (enter to replace; empty to clear)"
				}
				if s.OAuth.CallbackPort != 0 {
					port = strconv.Itoa(s.OAuth.CallbackPort)
				}
			}
			add("OAuth", "Client ID", id, "oauth:client_id")
			add("OAuth", "Client secret", secret, "oauth:client_secret")
			add("OAuth", "Scopes (comma-separated)", scopes, "oauth:scopes")
			add("OAuth", "Callback port", port, "oauth:callback_port")
		}
		add("actions", "Delete personal server", "", "delete")
		add("actions", "Back to servers", "", "back")
	}
	p.home()
	e.list = p
}
func (e *mcpEditor) change(field string, value any) {
	s := e.servers[e.name]
	switch field {
	case "disabled":
		s.Disabled = value.(bool)
	case "type":
		s.Type = value.(string)
		s.Command = ""
		s.Args = nil
		s.URL = ""
		s.Env = nil
		s.Headers = nil
		s.OAuth = nil
	case "command":
		s.Command = value.(string)
	case "args":
		s.Args = value.([]string)
	case "url":
		s.URL = value.(string)
	case "oauth:client_id", "oauth:client_secret", "oauth:scopes", "oauth:callback_port":
		if s.OAuth == nil {
			s.OAuth = &config.MCPOAuth{}
		}
		switch field {
		case "oauth:client_id":
			s.OAuth.ClientID, _ = value.(string)
		case "oauth:client_secret":
			s.OAuth.ClientSecret, _ = value.(string)
		case "oauth:scopes":
			s.OAuth.Scopes, _ = value.([]string)
		case "oauth:callback_port":
			s.OAuth.CallbackPort, _ = value.(int)
		}
	default:
		parts := strings.SplitN(field, ":", 2)
		var dest *map[string]string
		if parts[0] == "env" {
			dest = &s.Env
		} else {
			dest = &s.Headers
		}
		if *dest == nil {
			*dest = map[string]string{}
		}
		if value == nil {
			delete(*dest, parts[1])
		} else {
			(*dest)[parts[1]] = value.(string)
		}
	}
	e.servers[e.name] = s
	if e.changes[e.name] == nil {
		e.changes[e.name] = map[string]any{}
	}
	if field == "type" {
		// Drop obsolete draft fields and remove credentials from the old transport.
		e.changes[e.name] = map[string]any{"command": nil, "args": nil, "url": nil, "env": nil, "headers": nil, "oauth": nil}
	}
	e.changes[e.name][field] = value
	e.status, e.failed, e.closing = "Not saved yet", false, false
	e.rebuild()
}
func (m *model) openMCPEditor() tea.Cmd {
	m.settings = &settingsPanel{mcp: newMCPEditor(m.opts.Config)}
	return nil
}
func (e *mcpEditor) startInput(field string, width int) tea.Cmd {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.SetWidth(max(width-10, 20))
	e.editing = field
	s := e.servers[e.name]
	switch field {
	case "command":
		ti.SetValue(s.Command)
	case "url":
		ti.SetValue(s.URL)
	case "args":
		ti.SetValue(strings.Join(s.Args, ", "))
	case "oauth:client_id":
		if s.OAuth != nil {
			ti.SetValue(s.OAuth.ClientID)
		}
	case "oauth:scopes":
		if s.OAuth != nil {
			ti.SetValue(strings.Join(s.OAuth.Scopes, ", "))
		}
	case "oauth:callback_port":
		if s.OAuth != nil && s.OAuth.CallbackPort != 0 {
			ti.SetValue(strconv.Itoa(s.OAuth.CallbackPort))
		}
	}
	if field == "name" {
		ti.Placeholder = "unique server name"
	}
	if strings.HasSuffix(field, "+") {
		ti.Placeholder = "key name"
	}
	if field == "oauth:client_secret" || strings.HasPrefix(field, "env:") || strings.HasPrefix(field, "headers:") {
		ti.Placeholder = "replace value (empty clears)"
		ti.EchoMode = textinput.EchoPassword
	}
	e.input = &ti
	e.status = ""
	return ti.Focus()
}
func (e *mcpEditor) finishInput() {
	v := strings.TrimSpace(e.input.Value())
	field := e.editing
	if field == "name" {
		if v == "" || strings.ContainsAny(v, ". \t\n") {
			e.status, e.failed = "Enter a name without dots or spaces", true
			return
		}
		if _, ok := e.servers[v]; ok {
			e.status, e.failed = "Personal server already exists", true
			return
		}
		e.name = v
		e.servers[v] = config.MCPServer{Type: "stdio"}
		e.changes[v] = map[string]any{"type": "stdio"}
		e.input = nil
		e.status = "Not saved yet"
		e.rebuild()
		return
	}
	if strings.HasSuffix(field, "+") {
		if v == "" || strings.ContainsAny(v, "= \t\n") {
			e.status, e.failed = "Enter one key name", true
			return
		}
		e.startInput(strings.TrimSuffix(field, "+")+":"+v, max(e.input.Width()+10, 30))
		return
	}
	switch field {
	case "type":
		if v != "stdio" && v != "http" && v != "sse" {
			e.status, e.failed = "Choose stdio, http, or sse", true
			return
		}
	case "url":
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			e.status, e.failed = "Enter an http(s) URL", true
			return
		}
	case "oauth:callback_port":
		if v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 65535 {
				e.status, e.failed = "Port must be 1–65535 or empty (auto)", true
				return
			}
		}
	}
	e.input = nil
	switch field {
	case "args":
		e.change(field, lspWords(v))
	case "oauth:scopes":
		if v == "" {
			e.change(field, nil)
		} else {
			e.change(field, lspWords(v))
		}
	case "oauth:callback_port":
		n, _ := strconv.Atoi(v)
		if n == 0 {
			e.change(field, nil)
		} else {
			e.change(field, n)
		}
	default:
		if v == "" && (strings.HasPrefix(field, "env:") || strings.HasPrefix(field, "headers:") || field == "oauth:client_secret" || field == "oauth:client_id") {
			e.change(field, nil)
		} else {
			e.change(field, v)
		}
	}
}
func (m *model) saveMCPEditor() tea.Cmd {
	e := m.settings.mcp
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
			s := e.servers[name]
			if s.Transport() == "stdio" && s.Command == "" || s.Transport() != "stdio" && s.URL == "" {
				e.status, e.failed = fmt.Sprintf("%s needs a command or URL", name), true
				return nil
			}
		}
	}
	if err := m.opts.Config.PatchUserMCP(e.changes); err != nil {
		e.status, e.failed = "Couldn't save: "+err.Error(), true
		return nil
	}
	e.changes = map[string]map[string]any{}
	e.failed, e.closing = false, false
	if m.canReloadApp() {
		m.settings = nil
		return tea.Batch(m.reloadApp(), m.println(m.st.dim.Render("Saved to "+shortHome(m.opts.Config.UserConfigPath()))))
	}
	e.status = "Saved to " + shortHome(m.opts.Config.UserConfigPath()) + " · active after /reload"
	return nil
}
func (m *model) requestMCPSave() tea.Cmd {
	if len(m.settings.mcp.changes) > 0 && m.hasContext() && m.canReloadApp() {
		return m.askReload("MCP changes need rebuilt services and a fresh model context. Apply them? (Transcript stays saved)", m.saveMCPEditor)
	}
	return m.saveMCPEditor()
}
func (m *model) handleMCPKey(msg tea.KeyPressMsg) tea.Cmd {
	e := m.settings.mcp
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
		return m.requestMCPSave()
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
	case "type":
		return e.startInput(field, m.width)
	default:
		return e.startInput(field, m.width)
	}
	return nil
}
func (m *model) mcpEditorView() string {
	e := m.settings.mcp
	w := max(m.width-6, 20)
	head := spread(m.st.accent.Render("MCP servers"), m.st.dim.Render("personal · all projects"), w)
	var body, hint string
	if e.input != nil {
		body = m.st.dim.Render(e.editing) + "\n" + e.input.View()
		hint = "enter apply to draft · esc cancel"
	} else {
		e.list.height = max(m.availablePanelRows()-7-boolRows(e.status != ""), 1)
		body = e.list.view(m.st, w)
		hint = "↑/↓ move · enter select/change · s save · esc back/close"
	}
	out := head + "\n" + body + "\n" + m.st.dim.Render("Credentials are masked; enter a new value to replace, empty to clear")
	if e.status != "" {
		style := m.st.ok
		if e.failed {
			style = m.st.err
		}
		out += "\n" + style.Render(e.status)
	}
	return m.st.modal.Width(max(m.width-2, 10)).Render(out + "\n" + m.st.dim.Render(hint))
}
