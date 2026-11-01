package tui

import (
	"strings"
	"testing"
)

func TestReplayNeedsTheBrowser(t *testing.T) {
	m := testModel(t)
	msg := m.replayCommand("")()
	if out, ok := msg.(outputMsg); !ok || !strings.Contains(string(out), "browser tools are off") {
		t.Fatalf("%v", msg)
	}
}

func TestCompareNeedsTheBrowser(t *testing.T) {
	m := testModel(t)
	msg := m.compareCommand()()
	if out, ok := msg.(outputMsg); !ok || !strings.Contains(string(out), "browser tools are off") {
		t.Fatalf("%v", msg)
	}
}
