package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/agent"
)

// showSuggestion delivers a suggestion as if the model had just answered.
func showSuggestion(m *model, text string) {
	m.lastStop = "end_turn"
	if m.requestSuggestion() == nil {
		panic("no suggestion requested")
	}
	m.Update(suggestionMsg{gen: m.suggestGen, from: m.agent, text: text})
}

func TestSuggestionIsRequestedAfterANormalTurn(t *testing.T) {
	m := testModel(t)
	m.handleEvent(agent.Event{Kind: agent.EvDone, StopReason: "end_turn"})
	m.Update(runEndedMsg{})
	if m.suggestCancel == nil {
		t.Fatal("a turn that ended normally should ask for a suggestion")
	}

	for name, prep := range map[string]func(m *model){
		"interrupted": func(m *model) { m.lastStop = "interrupted" },
		"subagent done doesn't count": func(m *model) {
			m.lastStop = "interrupted"
			m.handleEvent(agent.Event{Kind: agent.EvDone, Agent: "worker: x", StopReason: "end_turn"})
		},
		"input typed": func(m *model) { m.lastStop = "end_turn"; typeText(m, "next") },
		"setting off": func(m *model) { m.lastStop = "end_turn"; m.suggest = false },
		"queued":      func(m *model) { m.lastStop = "end_turn"; m.queue = []string{"later"} },
	} {
		m := testModel(t)
		prep(m)
		if m.requestSuggestion() != nil || m.suggestCancel != nil {
			t.Errorf("%s: no suggestion should be requested", name)
		}
	}
}

func TestSuggestionShowsAndTabTakesIt(t *testing.T) {
	m := testModel(t)
	showSuggestion(m, "run the tests")
	if m.suggestion != "run the tests" || !strings.Contains(m.input.Placeholder, "run the tests") {
		t.Fatalf("suggestion %q placeholder %q", m.suggestion, m.input.Placeholder)
	}
	if m.suggestCancel != nil {
		t.Error("the request is finished once its answer arrives")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.input.Value(); got != "run the tests" {
		t.Fatalf("tab should fill the composer, got %q", got)
	}
	if m.suggestion != "" || m.input.Placeholder != composerPlaceholder {
		t.Fatalf("taking it clears it: %q / %q", m.suggestion, m.input.Placeholder)
	}
	typeText(m, " again")
	if got := m.input.Value(); got != "run the tests again" {
		t.Fatalf("the cursor should be at the end, got %q", got)
	}
}

func TestStaleSuggestionsAreDropped(t *testing.T) {
	m := testModel(t)
	m.lastStop = "end_turn"
	m.requestSuggestion()
	gen := m.suggestGen
	m.Update(suggestionMsg{gen: gen, from: agent.New(agent.Options{}), text: "from another session"})
	m.Update(suggestionMsg{gen: gen - 1, from: m.agent, text: "older"})
	if m.suggestion != "" {
		t.Fatalf("stale suggestion shown: %q", m.suggestion)
	}
	typeText(m, "my own")
	m.Update(suggestionMsg{gen: gen, from: m.agent, text: "too late"})
	if m.suggestion != "" {
		t.Fatalf("a suggestion must not replace typed text: %q", m.suggestion)
	}
}

func TestSubmittingCancelsAndClearsTheSuggestion(t *testing.T) {
	m := testModel(t)
	m.lastStop = "end_turn"
	m.requestSuggestion()
	canceled := false
	m.suggestCancel = func() { canceled = true }
	gen := m.suggestGen
	typeText(m, "/clear")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !canceled || m.suggestGen == gen {
		t.Fatalf("sending something should retire the request: canceled=%v", canceled)
	}

	showSuggestion(m, "commit this")
	m.command("/clear")
	if m.suggestion != "" || m.input.Placeholder != composerPlaceholder {
		t.Fatal("/clear should drop the suggestion")
	}
}

func TestPromptSuggestionsSetting(t *testing.T) {
	m := testModel(t)
	if !m.suggest {
		t.Fatal("prompt suggestions are on by default")
	}
	showSuggestion(m, "commit this")
	if _, _, err := m.saveSetting("prompt_suggestions", "off"); err != nil {
		t.Fatal(err)
	}
	if m.suggest || m.suggestion != "" || m.opts.Config.PromptSuggestionsOn() {
		t.Fatal("turning the setting off should hide the suggestion and stop asking")
	}
}
