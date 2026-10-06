package tui

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"larik/internal/config"
	"larik/internal/sandbox"
)

type sandboxField int

const (
	sandboxEnabled sandboxField = iota
	sandboxNetwork
	sandboxWritable
	sandboxDomain
)

type sandboxRef struct {
	field sandboxField
	index int
	add   bool
}

type sandboxEditor struct {
	scope   int
	user    sandbox.Config
	project sandbox.Config
	list    *picker
	input   *textinput.Model
	editing sandboxRef
	dirty   [2]bool
	status  string
	failed  bool
	closing bool
}

func cloneSandbox(c sandbox.Config) sandbox.Config {
	if c.Enabled != nil {
		on := *c.Enabled
		c.Enabled = &on
	}
	c.Writable = append([]string(nil), c.Writable...)
	c.AllowedDomains = append([]string(nil), c.AllowedDomains...)
	return c
}

func newSandboxEditor(cfg *config.Config) *sandboxEditor {
	e := &sandboxEditor{}
	var errs []string
	if c, err := config.SandboxAt(cfg.UserConfigPath()); err != nil {
		errs = append(errs, shortHome(cfg.UserConfigPath())+": "+err.Error())
	} else {
		e.user = cloneSandbox(c)
	}
	projectPath := config.LocalSettingsPath(cfg.Cwd)
	if c, err := config.SandboxAt(projectPath); err != nil {
		errs = append(errs, shortHome(projectPath)+": "+err.Error())
	} else {
		e.project = cloneSandbox(c)
	}
	if len(errs) > 0 {
		e.status, e.failed = strings.Join(errs, " · "), true
	}
	e.rebuild()
	return e
}

func (e *sandboxEditor) config() *sandbox.Config {
	if e.scope == 1 {
		return &e.project
	}
	return &e.user
}

func sandboxEnabledLabel(v *bool) string {
	if v == nil {
		return "default (on when available)"
	}
	if *v {
		return "on"
	}
	return "off"
}

func (e *sandboxEditor) rebuild() {
	c := e.config()
	p := &picker{}
	p.items = append(p.items,
		pickItem{section: "sandbox", label: "Enabled", detail: sandboxEnabledLabel(c.Enabled), value: sandboxRef{field: sandboxEnabled}},
		pickItem{section: "sandbox", label: "Full network access", detail: onOff(c.Network), note: "widens access", warn: c.Network, value: sandboxRef{field: sandboxNetwork}},
	)
	for i, path := range c.Writable {
		p.items = append(p.items, pickItem{section: "extra writable paths", label: path, detail: "commands may write here", warn: true, value: sandboxRef{field: sandboxWritable, index: i}})
	}
	p.items = append(p.items, pickItem{section: "extra writable paths", label: "+ Add writable path…", note: "widens access", warn: true, value: sandboxRef{field: sandboxWritable, add: true}})
	for i, domain := range c.AllowedDomains {
		p.items = append(p.items, pickItem{section: "allowed domains", label: domain, detail: "and its subdomains", warn: true, value: sandboxRef{field: sandboxDomain, index: i}})
	}
	p.items = append(p.items, pickItem{section: "allowed domains", label: "+ Add allowed domain…", note: "widens access", warn: true, value: sandboxRef{field: sandboxDomain, add: true}})
	p.home()
	e.list = p
}

func (m *model) openSandboxEditor() tea.Cmd {
	m.settings = &settingsPanel{sandbox: newSandboxEditor(m.opts.Config)}
	return nil
}

func (e *sandboxEditor) cycleEnabled() {
	c := e.config()
	switch {
	case c.Enabled == nil:
		on := true
		c.Enabled = &on
	case *c.Enabled:
		off := false
		c.Enabled = &off
	default:
		c.Enabled = nil
	}
	e.dirty[e.scope], e.status, e.failed = true, "Not saved yet", false
	e.rebuild()
}

func (e *sandboxEditor) startInput(ref sandboxRef, width int) tea.Cmd {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.SetWidth(max(width-10, 20))
	c := e.config()
	if ref.field == sandboxWritable {
		ti.Placeholder = "absolute path or ~/path"
		if !ref.add {
			ti.SetValue(c.Writable[ref.index])
		}
	} else {
		ti.Placeholder = "example.com"
		if !ref.add {
			ti.SetValue(c.AllowedDomains[ref.index])
		}
	}
	e.editing, e.input, e.status = ref, &ti, ""
	return ti.Focus()
}

func validateSandboxValue(field sandboxField, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("value must not be empty")
	}
	if field == sandboxWritable {
		if strings.ContainsRune(value, '\x00') {
			return "", fmt.Errorf("path contains an invalid NUL character")
		}
		if strings.HasPrefix(value, "~/") {
			return value, nil
		}
		if !filepath.IsAbs(value) {
			return "", fmt.Errorf("writable path must be absolute or start with ~/")
		}
		return filepath.Clean(value), nil
	}
	if strings.ContainsAny(value, " /@") || strings.Contains(value, "://") {
		return "", fmt.Errorf("domain must be a hostname such as example.com")
	}
	u, err := url.Parse("https://" + value)
	if err != nil || u.Hostname() != value || u.Port() != "" {
		return "", fmt.Errorf("domain must be a hostname such as example.com")
	}
	return strings.ToLower(value), nil
}

func (e *sandboxEditor) finishInput() {
	value, err := validateSandboxValue(e.editing.field, e.input.Value())
	if err != nil {
		e.status, e.failed = err.Error(), true
		return
	}
	c, ref := e.config(), e.editing
	if ref.field == sandboxWritable {
		if ref.add {
			c.Writable = append(c.Writable, value)
		} else {
			c.Writable[ref.index] = value
		}
	} else if ref.add {
		c.AllowedDomains = append(c.AllowedDomains, value)
	} else {
		c.AllowedDomains[ref.index] = value
	}
	e.input, e.dirty[e.scope], e.failed = nil, true, false
	e.status = "Not saved yet · this widens sandbox access"
	e.rebuild()
}

func (e *sandboxEditor) removeSelected() {
	it, ok := e.list.selected()
	if !ok {
		return
	}
	ref := it.value.(sandboxRef)
	if ref.add || ref.field < sandboxWritable {
		return
	}
	c := e.config()
	if ref.field == sandboxWritable {
		c.Writable = append(c.Writable[:ref.index], c.Writable[ref.index+1:]...)
	} else {
		c.AllowedDomains = append(c.AllowedDomains[:ref.index], c.AllowedDomains[ref.index+1:]...)
	}
	e.dirty[e.scope], e.status, e.failed = true, "Removed · not saved yet", false
	e.rebuild()
}

func sandboxEmpty(c sandbox.Config) bool {
	return c.Enabled == nil && !c.Network && len(c.Writable) == 0 && len(c.AllowedDomains) == 0
}

func (m *model) saveSandboxEditor() tea.Cmd {
	e := m.settings.sandbox
	cfg := cloneSandbox(*e.config())
	var value any = cfg
	if sandboxEmpty(cfg) {
		value = nil
	}
	var path string
	var err error
	if e.scope == 1 {
		path = config.LocalSettingsPath(m.opts.Config.Cwd)
		err = m.opts.Config.SetProjectSettingPath("sandbox", value)
	} else {
		path = m.opts.Config.UserConfigPath()
		err = m.opts.Config.SetUserSettingPath("sandbox", value)
	}
	if err != nil {
		e.status, e.failed = "Couldn't save: "+err.Error(), true
		return nil
	}
	e.dirty[e.scope], e.failed, e.closing = false, false, false
	if m.canReloadApp() {
		m.settings = nil
		return tea.Batch(m.reloadApp(), m.println(m.st.dim.Render(savedAndReloaded("Bash sandbox", path))))
	}
	e.status = savedStatus(path, savedAfterReload)
	return nil
}

func (m *model) requestSandboxSave() tea.Cmd {
	if m.hasContext() && m.canReloadApp() {
		return m.askReload("Sandbox changes need rebuilt services. Clear the current model context and apply them? (Transcript stays saved)", m.saveSandboxEditor)
	}
	return m.saveSandboxEditor()
}

func (m *model) handleSandboxEditorKey(msg tea.KeyPressMsg) tea.Cmd {
	e := m.settings.sandbox
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
		if e.dirty[e.scope] && !e.closing {
			e.status, e.failed, e.closing = "Unsaved changes · s save · esc discard", true, true
			return nil
		}
		m.settings = nil
		return nil
	case "tab":
		if e.dirty[e.scope] {
			e.status, e.failed = "Save or discard changes before switching scope", true
			return nil
		}
		e.scope = 1 - e.scope
		e.status, e.failed, e.closing = "", false, false
		e.rebuild()
		return nil
	case "s", "ctrl+s":
		return m.requestSandboxSave()
	case "d", "backspace", "delete":
		e.removeSelected()
		return nil
	}
	if !e.list.handleKey(msg) {
		return nil
	}
	it, _ := e.list.selected()
	ref := it.value.(sandboxRef)
	switch ref.field {
	case sandboxEnabled:
		e.cycleEnabled()
	case sandboxNetwork:
		c := e.config()
		c.Network = !c.Network
		e.dirty[e.scope], e.failed = true, false
		e.status = "Not saved yet · full network access widens sandbox access"
		e.rebuild()
	default:
		return e.startInput(ref, m.width)
	}
	return nil
}

func (m *model) sandboxEditorView() string {
	e := m.settings.sandbox
	w := max(m.width-6, 20)
	scope := "all projects · personal"
	if e.scope == 1 {
		scope = "this project · private"
	}
	head := spread(m.st.accent.Render("Bash sandbox"), m.st.dim.Render(scope), w)
	var body, hint string
	if e.input != nil {
		label := "Writable path"
		if e.editing.field == sandboxDomain {
			label = "Allowed domain"
		}
		body = m.st.warn.Render("This setting widens command access") + "\n" + m.st.dim.Render(label) + "\n" + e.input.View()
		hint = "enter apply to draft · esc cancel"
	} else {
		e.list.height = max(m.availablePanelRows()-5-boolRows(e.status != ""), 1)
		body = e.list.view(m.st, w)
		hint = "↑/↓ move · enter change · d delete · s save · tab scope · esc close"
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
