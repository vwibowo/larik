package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"larik/internal/config"
)

type displayField int

const (
	displayCommand displayField = iota
	displayInterval
	displayPreview
	displayDisable
)

type displayEditor struct {
	key, title string
	command    string
	interval   int
	list       *picker
	input      *textinput.Model
	editing    displayField
	preview    []string
	status     string
	failed     bool
	dirty      bool
	closing    bool
}

type displayPreviewMsg struct {
	key   string
	lines []string
	err   error
}

func newDisplayEditor(cfg *config.Config, key, title string) *displayEditor {
	e := &displayEditor{key: key, title: title}
	current, err := config.StatusLineAt(cfg.UserConfigPath(), key)
	if err != nil {
		e.status, e.failed = err.Error(), true
	} else if current != nil {
		e.command, e.interval = current.Command, current.RefreshIntervalMS
	}
	e.rebuild()
	return e
}

func (e *displayEditor) rebuild() {
	interval := "default · 1000 ms"
	if e.interval > 0 {
		interval = fmt.Sprintf("%d ms", e.interval)
	}
	command := e.command
	if command == "" {
		command = "not configured"
	}
	e.list = &picker{items: []pickItem{
		{section: "configuration", label: "Command", detail: command, note: "runs on your machine", warn: true, value: displayCommand},
		{section: "configuration", label: "Refresh interval", detail: interval, value: displayInterval},
		{section: "actions", label: "Test and preview…", detail: "runs the draft once with current session JSON", value: displayPreview},
		{section: "actions", label: "Disable", detail: "remove this personal setting", value: displayDisable},
	}}
	e.list.home()
}

func (m *model) openDisplayEditor(key, title string) tea.Cmd {
	m.settings = &settingsPanel{display: newDisplayEditor(m.opts.Config, key, title)}
	return nil
}

func (m *model) openStatusLineEditor() tea.Cmd {
	return m.openDisplayEditor("status_line", "Custom status line")
}

func (m *model) openSidebarEditor() tea.Cmd {
	return m.openDisplayEditor("sidebar", "Custom sidebar")
}

func (e *displayEditor) startInput(field displayField, width int) tea.Cmd {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.SetWidth(max(width-10, 20))
	switch field {
	case displayCommand:
		ti.Placeholder = "shell command"
		ti.SetValue(e.command)
	case displayInterval:
		ti.Placeholder = "300–60000 ms; empty for 1000"
		if e.interval > 0 {
			ti.SetValue(strconv.Itoa(e.interval))
		}
	}
	e.editing, e.input, e.status = field, &ti, ""
	return ti.Focus()
}

func (e *displayEditor) finishInput() {
	value := strings.TrimSpace(e.input.Value())
	switch e.editing {
	case displayCommand:
		e.command = value
	case displayInterval:
		if value == "" || strings.EqualFold(value, "default") {
			e.interval = 0
			break
		}
		n, err := strconv.Atoi(value)
		if err != nil || n < 300 || n > 60000 {
			e.status, e.failed = "Refresh interval must be 300–60000 ms, or empty for the default", true
			return
		}
		e.interval = n
	}
	e.input, e.dirty, e.failed = nil, true, false
	e.status = "Not saved yet"
	e.rebuild()
}

func (m *model) previewDisplay() tea.Cmd {
	e := m.settings.display
	if strings.TrimSpace(e.command) == "" {
		e.status, e.failed = "Set a command before testing it", true
		return nil
	}
	command, key := e.command, e.key
	dir, input, maxLines := m.opts.Config.Cwd, m.statusPayload(), statusMaxLines
	if key == "sidebar" {
		maxLines = sidebarMaxLines
	}
	e.status, e.failed, e.preview = "Running preview…", false, nil
	return func() tea.Msg {
		lines, err := runStatusLimit(command, dir, input, maxLines)
		return displayPreviewMsg{key: key, lines: lines, err: err}
	}
}

func (m *model) displayPreviewDone(msg displayPreviewMsg) {
	if m.settings == nil || m.settings.display == nil || m.settings.display.key != msg.key {
		return
	}
	e := m.settings.display
	if msg.err != nil {
		e.status, e.failed = "Preview failed: "+msg.err.Error(), true
		e.preview = nil
		return
	}
	e.preview, e.failed = msg.lines, false
	e.status = "Preview succeeded"
}

func (m *model) saveDisplay() tea.Cmd {
	e := m.settings.display
	var value any
	if strings.TrimSpace(e.command) != "" {
		value = &config.StatusLine{Type: "command", Command: e.command, RefreshIntervalMS: e.interval}
	}
	if err := m.opts.Config.SetUserSettingPath(e.key, value); err != nil {
		e.status, e.failed = "Couldn't save: "+err.Error(), true
		return nil
	}
	var active *statusCmd
	if value != nil {
		active = newStatusCmd(e.command, e.interval)
	}
	if e.key == "sidebar" {
		if value == nil {
			m.opts.Config.Sidebar = nil
		} else {
			m.opts.Config.Sidebar = value.(*config.StatusLine)
		}
		m.sidebarStatus = active
	} else {
		if value == nil {
			m.opts.Config.StatusLine = nil
		} else {
			m.opts.Config.StatusLine = value.(*config.StatusLine)
		}
		m.status = active
	}
	e.dirty, e.closing, e.failed = false, false, false
	e.status = "Saved to " + shortHome(m.opts.Config.UserConfigPath()) + " · active now"
	return m.statusTick()
}

func (m *model) handleDisplayKey(msg tea.KeyPressMsg) tea.Cmd {
	e := m.settings.display
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
		if e.dirty && !e.closing {
			e.status, e.failed, e.closing = "Unsaved changes · s save · esc discard", true, true
			return nil
		}
		m.settings = nil
		return nil
	case "s", "ctrl+s":
		return m.saveDisplay()
	case "t":
		return m.previewDisplay()
	}
	if !e.list.handleKey(msg) {
		return nil
	}
	it, _ := e.list.selected()
	switch it.value.(displayField) {
	case displayCommand:
		return e.startInput(displayCommand, m.width)
	case displayInterval:
		return e.startInput(displayInterval, m.width)
	case displayPreview:
		return m.previewDisplay()
	case displayDisable:
		e.command, e.interval, e.preview = "", 0, nil
		e.dirty, e.status, e.failed = true, "Disabled in draft · s to save", false
		e.rebuild()
	}
	return nil
}

func (m *model) displayEditorView() string {
	e := m.settings.display
	w := max(m.width-6, 20)
	head := spread(m.st.accent.Render(e.title), m.st.dim.Render("personal · runs a shell command"), w)
	var body, hint string
	if e.input != nil {
		label := "Command"
		if e.editing == displayInterval {
			label = "Refresh interval"
		}
		body = m.st.dim.Render(label) + "\n" + e.input.View()
		hint = "enter apply to draft · esc cancel"
	} else {
		e.list.height = max(m.availablePanelRows()-6-boolRows(e.status != ""), 1)
		body = e.list.view(m.st, w)
		if len(e.preview) > 0 {
			body += "\n" + m.st.dim.Render("Preview") + "\n" + strings.Join(e.preview, "\n")
		}
		hint = "↑/↓ move · enter edit/run · t test · s save · esc close"
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
