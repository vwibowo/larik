package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"larik/internal/agent"
)

// Prompt suggestions: after a turn ends, the model predicts the next
// prompt, shown as ghost text in the empty composer; tab takes it.

const composerPlaceholder = "Ask larik…  (/ commands · @ files · ! shell)"

// suggestionMsg carries a suggestion back; gen and from discard one that
// arrives after the conversation has moved on.
type suggestionMsg struct {
	gen  int
	from *agent.Agent
	text string
}

// requestSuggestion asks for a suggestion after a turn that ended
// normally, when the setting is on and nothing is typed yet.
func (m *model) requestSuggestion() tea.Cmd {
	m.clearSuggestion()
	if !m.suggest || m.lastStop != "end_turn" || m.input.Value() != "" || !m.idle() || len(m.queue) > 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.suggestCancel = cancel
	gen, a := m.suggestGen, m.agent
	return func() tea.Msg {
		text, _ := a.SuggestNext(ctx) // a failed suggestion isn't worth reporting
		return suggestionMsg{gen: gen, from: a, text: text}
	}
}

// suggestionArrived shows a suggestion that is still current.
func (m *model) suggestionArrived(msg suggestionMsg) {
	if msg.gen != m.suggestGen || msg.from != m.agent {
		return
	}
	if m.suggestCancel != nil {
		m.suggestCancel()
		m.suggestCancel = nil
	}
	m.stats = m.agent.Stats() // the request's spend
	if msg.text == "" || !m.idle() || m.input.Value() != "" {
		return
	}
	m.suggestion = msg.text
	m.input.Placeholder = msg.text + "  (tab to use)"
}

// clearSuggestion drops the suggestion shown or on its way.
func (m *model) clearSuggestion() {
	m.suggestGen++
	if m.suggestCancel != nil {
		m.suggestCancel()
		m.suggestCancel = nil
	}
	m.suggestion = ""
	m.input.Placeholder = composerPlaceholder
}

// acceptSuggestion puts the suggestion in the composer for editing or
// sending. It reports whether there was one to take.
func (m *model) acceptSuggestion() bool {
	if m.suggestion == "" || m.input.Value() != "" {
		return false
	}
	text := m.suggestion
	m.clearSuggestion()
	m.input.SetValue(text)
	m.input.MoveToEnd()
	return true
}
