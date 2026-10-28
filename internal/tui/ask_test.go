package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/agent"
	"larik/internal/permission"
)

func askPrompt(reply chan agent.PermissionReply) agent.Event {
	return agent.Event{Kind: agent.EvPermission, ToolName: permission.AskUserTool, Reply: reply,
		Input: json.RawMessage(`{"question":"Which config format?","options":[{"label":"TOML","description":"easy to edit by hand"},{"label":"JSON"}]}`)}
}

func TestAskPrompt(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 100, 40
	reply := make(chan agent.PermissionReply, 1)

	m.handleEvent(askPrompt(reply))
	v := plain(m.permissionView())
	for _, want := range []string{"Larik has a question", "Which config format?", "1. TOML", "easy to edit by hand", "2. JSON", "3. Type something else", "Waiting for your answer"} {
		if !strings.Contains(v, want) {
			t.Errorf("prompt lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "don't ask again") {
		t.Error("a question offers no standing rule")
	}

	out := plain(printed(m.handlePermissionKey(tea.KeyPressMsg{Code: '2', Text: "2"})))
	if r := <-reply; !r.Allow || r.Answer != "JSON" {
		t.Fatalf("2: %+v", r)
	}
	if !strings.Contains(out, "answered: JSON") {
		t.Errorf("confirmation %q", out)
	}

	m.handleEvent(askPrompt(reply))
	m.handlePermissionKey(tea.KeyPressMsg{Code: tea.KeyDown})
	m.handlePermissionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if r := <-reply; r.Answer != "JSON" {
		t.Fatalf("down, enter: %+v", r)
	}

	m.handleEvent(askPrompt(reply))
	m.handlePermissionKey(tea.KeyPressMsg{Code: '3', Text: "3"})
	if m.permFeedback == nil || m.permFeedback.Placeholder != "Your answer" {
		t.Fatalf("3 should open the answer field: %+v", m.permFeedback)
	}
	m.handlePermissionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.perm == nil {
		t.Fatal("an empty answer should not be sent")
	}
	m.handlePermissionKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.permFeedback != nil || m.perm == nil {
		t.Fatal("esc in the answer field should go back to the options")
	}
	m.handlePermissionKey(tea.KeyPressMsg{Code: '3', Text: "3"})
	m.permFeedback.SetValue("YAML, please")
	m.handlePermissionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if r := <-reply; !r.Allow || r.Answer != "YAML, please" {
		t.Fatalf("typed: %+v", r)
	}

	m.handleEvent(askPrompt(reply))
	out = plain(printed(m.handlePermissionKey(tea.KeyPressMsg{Code: tea.KeyEscape})))
	if r := <-reply; r.Allow {
		t.Fatalf("esc: %+v", r)
	}
	if !strings.Contains(out, "dismissed") {
		t.Errorf("confirmation %q", out)
	}
}
