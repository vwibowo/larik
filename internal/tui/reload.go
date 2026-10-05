package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// reloadPrompt is a confirmation for an operation that changes the active
// context. The callback is only invoked after an explicit yes.
type reloadPrompt struct {
	message string
	apply   func() tea.Cmd
}

func (m *model) askReload(message string, apply func() tea.Cmd) tea.Cmd {
	m.reload = &reloadPrompt{message: message, apply: apply}
	return nil
}

func (m *model) reloadView() string {
	return m.st.modal.Width(max(m.width-2, 10)).Render(m.reload.message + "\n" + m.st.dim.Render("y/enter apply · n/esc cancel"))
}

func (m *model) handleReloadKey(msg tea.KeyPressMsg) tea.Cmd {
	p := m.reload
	switch msg.String() {
	case "y", "enter":
		m.reload = nil
		return p.apply()
	case "n", "esc", "ctrl+c":
		m.reload = nil
	}
	return nil
}

func (m *model) hasContext() bool {
	return m.agent.HasContext()
}

// settingChanges reports whether saving value for spec would actually change
// anything that needs applying. Saving the value a setting already has is not
// a change, so it never costs you a conversation.
func (m *model) settingChanges(spec settingSpec, value string) bool {
	return spec.applies != applyNow && strings.TrimSpace(value) != spec.get(m)
}

// requiresFreshContext reports whether the change would discard a model
// context, which is what you are asked about before it happens.
func (m *model) requiresFreshContext(spec settingSpec, value string) bool {
	return m.settingChanges(spec, value) && m.hasContext()
}

// canReloadApp reports whether app services can be rebuilt right now. They
// cannot for a session someone else manages, or while work is in flight.
func (m *model) canReloadApp() bool {
	return m.opts.App != nil && m.sess != nil && m.idle() && m.agent.RunningBackground() == 0 && m.perm == nil
}

// applySetting puts a saved setting into force: clearing the model context,
// and rebuilding services when they are built from it. It returns anything
// still to run, and whether the setting is in force now.
func (m *model) applySetting(spec settingSpec) (tea.Cmd, bool) {
	switch spec.applies {
	case applyReload:
		if !m.canReloadApp() {
			return nil, false
		}
		return m.reloadApp(), true
	case applyFresh:
		m.clearForSetting()
	}
	return nil, true
}

// settingNote says when a saved setting takes effect: its own note, or, for
// one that could not be applied yet, what it is waiting for.
func (m *model) settingNote(spec settingSpec, applied bool) string {
	if applied {
		return spec.later
	}
	if spec.applies == applyReload && !m.idle() {
		return "active after the turn ends and you run /reload"
	}
	return "active after /reload or in a new session"
}

func (m *model) clearForSetting() {
	if !m.hasContext() {
		return
	}
	m.agent.Clear()
	m.stats = m.agent.Stats()
	m.todos = nil
	m.resetConversation()
}

func (m *model) confirmSetting(key, raw string) tea.Cmd {
	spec, ok := settingByKey(key)
	if !ok {
		return nil
	}
	v, err := spec.check(raw)
	if err != nil {
		return m.finishEdit(key, raw) // the panel reports why
	}
	if busy := m.busySetting(spec, v); busy != "" {
		m.settings.status, m.settings.failed = busy, true
		m.settings.editing, m.settings.values, m.settings.input = "", nil, nil
		return nil
	}
	if m.requiresFreshContext(spec, v) {
		return m.askReload(freshContextQuestion(spec), func() tea.Cmd { return m.finishEdit(key, raw) })
	}
	return m.finishEdit(key, raw)
}
