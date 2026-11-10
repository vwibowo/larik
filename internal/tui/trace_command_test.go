package tui

import (
	"strings"
	"testing"
)

func TestTraceBrowserPanelModeIsRemoved(t *testing.T) {
	m := testModel(t)
	cmd := m.command("/trace browser")
	if cmd == nil {
		t.Fatal("expected usage feedback")
	}
	msg, ok := cmd().(outputMsg)
	if !ok || !strings.Contains(string(msg), "usage: /trace [external]") {
		t.Fatalf("unexpected feedback: %T %v", msg, msg)
	}
}
