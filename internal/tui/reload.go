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

func (m *model) requiresFreshContext(spec settingSpec, value string) bool {
	return (spec.key == "language" || spec.key == "execution") && strings.TrimSpace(value) != spec.get(m) && m.hasContext()
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
		m.finishEdit(key, raw)
		return nil
	}
	if m.requiresFreshContext(spec, v) {
		return m.askReload(spec.title+" needs a fresh context. Clear the current model context and apply it? (Transcript stays saved)", func() tea.Cmd {
			m.finishEdit(key, raw)
			if !m.settings.failed {
				m.clearForSetting()
			}
			return nil
		})
	}
	m.finishEdit(key, raw)
	return nil
}
