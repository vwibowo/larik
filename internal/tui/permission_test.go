package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"larik/internal/agent"
)

func permEvent(tool string, input any) *agent.Event {
	b, _ := json.Marshal(input)
	return &agent.Event{Kind: agent.EvPermission, ToolName: tool, Input: b, SuggestedRule: "bash(go test*)"}
}

func TestPermissionPromptBash(t *testing.T) {
	m := testModel(t)
	m.width = 110
	m.perm = permEvent("bash", map[string]any{"command": "go test ./...", "sandbox": false})
	m.perm.Agent = "reviewer"
	m.permQueue = []agent.Event{{}}
	v := plain(m.permissionView())
	for _, want := range []string{
		"Run this command?", "bash (unsandboxed) · from subagent reviewer · 1 more waiting",
		"$ go test ./...", "1. Yes", "2. Yes, and don't ask again for bash(go test*)",
		"3. No, tell larik what to do instead", "ctrl+c deny and stop",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("prompt lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "saved to") {
		t.Error("the save note shows only when option 2 is selected")
	}
	m.permIdx = 1
	if !strings.Contains(plain(m.permissionView()), "save to private project settings") {
		t.Error("option 2 selected should say where the rule is saved")
	}
}

func TestPermissionQuestionNamesTheAction(t *testing.T) {
	m := testModel(t)
	cwd := m.opts.Config.Cwd
	os.WriteFile(filepath.Join(cwd, "old.txt"), []byte("x"), 0o644)
	cases := []struct {
		e    *agent.Event
		want string
	}{
		{permEvent("write", map[string]any{"path": "new.txt", "content": "hi"}), "Create this file?"},
		{permEvent("write", map[string]any{"path": "old.txt", "content": "hi"}), "Overwrite this file?"},
		{permEvent("edit", map[string]any{"path": "a.go"}), "Make this edit?"},
		{permEvent("web_fetch", map[string]any{"url": "https://x"}), "Fetch this page?"},
		{permEvent("mcp__github__create_issue", map[string]any{"title": "t"}), "Use github › create_issue?"},
	}
	for _, c := range cases {
		if got := m.permQuestion(c.e); got != c.want {
			t.Errorf("%s: got %q, want %q", c.e.ToolName, got, c.want)
		}
	}
	if d := plain(m.permDetail(cases[0].e)); !strings.Contains(d, "1 line") || strings.Contains(d, "1 lines") {
		t.Errorf("write detail: %q", d)
	}
	if d := plain(m.permDetail(cases[4].e)); !strings.Contains(d, `"title": "t"`) {
		t.Errorf("other tools show their arguments as indented JSON: %q", d)
	}
}

func TestStatusLineWhileAPromptWaits(t *testing.T) {
	m := testModel(t)
	m.running, m.turnStart = true, time.Now()
	m.perm = permEvent("bash", map[string]any{"command": "ls"})
	v := plain(m.liveView())
	if !strings.Contains(v, "Waiting for your answer…") || strings.Contains(v, "esc to interrupt") {
		t.Fatalf("status while a prompt waits: %q", v)
	}
}

func TestPermissionDenialSendsFeedback(t *testing.T) {
	m := testModel(t)
	replies := make(chan agent.PermissionReply, 1)
	m.perm = permEvent("bash", map[string]any{"command": "make deploy"})
	m.perm.Reply = replies
	m.handlePermissionKey(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if m.permFeedback == nil || !strings.Contains(plain(m.permissionView()), "Tell larik what to do instead") {
		t.Fatal("denial did not open feedback input")
	}
	for _, r := range "Run tests first" {
		m.handlePermissionKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m.handlePermissionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := <-replies; got.Allow || got.Reason != "Run tests first" {
		t.Fatalf("denial reply = %+v", got)
	}
}
