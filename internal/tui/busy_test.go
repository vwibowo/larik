package tui

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"larik/internal/agent"
)

// printed runs cmd and returns the output it prints, if any.
func printed(cmd tea.Cmd) string {
	if cmd == nil {
		return ""
	}
	if s, ok := cmd().(outputMsg); ok {
		return string(s)
	}
	return ""
}

func TestPromptsWaitForCompaction(t *testing.T) {
	m := testModel(t)
	canceled := false
	m.compactCancel = func() { canceled = true }

	typeText(m, "hello")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.running || len(m.queue) != 1 || m.queue[0] != "hello" {
		t.Fatalf("a prompt during compaction must be queued: running=%v queue=%q", m.running, m.queue)
	}
	for _, c := range []string{"/clear", "/compact", "/new", "/model"} {
		if out := printed(m.command(c)); !strings.Contains(out, "being compacted") {
			t.Errorf("%s during compaction: %q", c, plain(out))
		}
	}
	if m.deliverBackground() != nil {
		t.Error("background results must not start a turn during compaction")
	}

	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !canceled {
		t.Fatal("esc must cancel compaction")
	}
	m.Update(compactedMsg{err: context.Canceled})
	if m.compactCancel != nil || m.busyLabel != "" {
		t.Fatal("compaction state must clear when it ends")
	}
	if !m.running || len(m.queue) != 0 {
		t.Fatalf("the queued prompt must be sent once compaction ends: running=%v queue=%q", m.running, m.queue)
	}
	m.cancel()
}

func TestCtrlCStopsShellCommand(t *testing.T) {
	m := testModel(t)
	stopped := false
	m.shellCancel = func() { stopped = true }
	m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !stopped || m.quitArmed {
		t.Fatalf("ctrl+c must stop the ! command (stopped=%v quitArmed=%v)", stopped, m.quitArmed)
	}
	if out := printed(m.command("/new")); !strings.Contains(out, "! command is running") {
		t.Errorf("/new during a ! command: %q", plain(out))
	}
}

func TestPasteGoesToTheOpenPanel(t *testing.T) {
	m := testModel(t)
	m.openHistoryPicker()
	m.Update(tea.PasteMsg{Content: "fix the\nbug"})
	if m.histPick.filter != "fix the bug" || m.input.Value() != "" {
		t.Fatalf("paste into history search: filter %q, composer %q", m.histPick.filter, m.input.Value())
	}
	m.histPick = nil

	m.openSettings("language")
	m.Update(tea.PasteMsg{Content: "Indonesian"})
	if got := m.settings.input.Value(); got != "Indonesian" || m.input.Value() != "" {
		t.Fatalf("paste into a settings field: field %q, composer %q", got, m.input.Value())
	}
	m.settings = nil

	m.perm = permEvent("bash", map[string]any{"command": "ls"})
	m.Update(tea.PasteMsg{Content: "stray"})
	if m.input.Value() != "" {
		t.Fatalf("a paste during a permission prompt reached the composer: %q", m.input.Value())
	}
}

func TestToolTitleTruncatesByCharacter(t *testing.T) {
	title := toolTitle("bash", []byte(`{"command":"echo `+strings.Repeat("é", 100)+`"}`), nil)
	if !utf8.ValidString(title) || !strings.HasSuffix(title, "…)") {
		t.Fatalf("title %q", title)
	}
}

func TestEndedBackgroundTaskClosesItsPrompt(t *testing.T) {
	m := testModel(t)
	reply := make(chan agent.PermissionReply, 1)
	other := make(chan agent.PermissionReply, 1)
	m.bgReplies = map[chan<- agent.PermissionReply]bool{reply: true, other: true}
	m.perm = &agent.Event{Kind: agent.EvPermission, ToolName: "bash", Agent: "worker: build", Reply: reply}
	m.permQueue = []agent.Event{{Kind: agent.EvPermission, ToolName: "bash", Agent: "explore: docs", Reply: other}}
	m.Update(bgEventMsg{agent.Event{Kind: agent.EvTaskDone, ToolID: "bg1", Agent: "worker: build", StopReason: "stopped"}, m.agent})
	if m.perm == nil || m.perm.Agent != "explore: docs" || len(m.permQueue) != 0 {
		t.Fatalf("the ended task's prompt should close and the next one show: %+v queue %d", m.perm, len(m.permQueue))
	}
}
