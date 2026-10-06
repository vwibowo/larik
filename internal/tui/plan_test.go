package tui

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/agent"
	"larik/internal/permission"
)

func planPrompt(reply chan agent.PermissionReply) agent.Event {
	return agent.Event{Kind: agent.EvPermission, ToolName: permission.ExitPlanTool, Reply: reply,
		Input: json.RawMessage(`{"plan":"## Fix the parser\n1. Handle empty input\n2. Add a test"}`)}
}

func TestPlanPrompt(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 100, 40
	reply := make(chan agent.PermissionReply, 1)
	cmd := m.handleEvent(planPrompt(reply))
	if out := plain(runSequence(cmd)); !strings.Contains(out, "Plan") || !strings.Contains(out, "Handle empty input") {
		t.Fatalf("the plan should be printed into the conversation:\n%s", out)
	}
	v := plain(m.permissionView())
	for _, want := range []string{"Ready to start on this plan?", "leaves plan mode", "Yes, and accept edits", "Yes, but ask before each edit", "No, keep planning", "shown above"} {
		if !strings.Contains(v, want) {
			t.Errorf("prompt lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "don't ask again") {
		t.Error("a plan prompt offers no standing rule")
	}

	out := plain(printed(m.handlePermissionKey(tea.KeyPressMsg{Code: 'y', Text: "y"})))
	if r := <-reply; !r.Allow || r.Mode != permission.ModeAcceptEdits || r.Always {
		t.Fatalf("y: %+v", r)
	}
	if !strings.Contains(out, "plan approved") || !strings.Contains(out, "accept edits") {
		t.Errorf("confirmation %q", out)
	}

	m.handleEvent(planPrompt(reply))
	m.handlePermissionKey(tea.KeyPressMsg{Code: '2', Text: "2"})
	if r := <-reply; !r.Allow || r.Mode != permission.ModeDefault {
		t.Fatalf("2: %+v", r)
	}

	m.handleEvent(planPrompt(reply))
	m.handlePermissionKey(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if !strings.Contains(m.permFeedback.Placeholder, "change in the plan") {
		t.Errorf("feedback placeholder %q", m.permFeedback.Placeholder)
	}
	m.permFeedback.SetValue("smaller steps")
	m.handlePermissionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if r := <-reply; r.Allow || r.Reason != "smaller steps" {
		t.Fatalf("n: %+v", r)
	}
}

// runSequence runs cmd and the commands it batches or sequences, and
// returns what they print.
func runSequence(cmd tea.Cmd) string {
	if cmd == nil {
		return ""
	}
	msg := cmd()
	switch msg := msg.(type) {
	case outputMsg:
		return string(msg)
	case renderedOutputMsg:
		return msg.text
	}
	// tea.Batch and tea.Sequence messages are slices of commands (the
	// latter of an unexported type).
	var b strings.Builder
	if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice {
		for i := range v.Len() {
			if c, ok := v.Index(i).Interface().(tea.Cmd); ok {
				b.WriteString(runSequence(c))
			}
		}
	}
	return b.String()
}
