package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"larik/internal/agent"
	"larik/internal/permission"
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
	if !strings.Contains(v, "Waiting for your approval") || strings.Contains(v, "esc to interrupt") {
		t.Fatalf("status while a prompt waits: %q", v)
	}
}

// Fixed old request times make this independent of tick timing and prove that
// promoting a queued prompt does not restart its approval timer.
func TestQueuedPermissionWaitUsesRequestCreation(t *testing.T) {
	m := testModel(t)
	m.width = 140
	first := *permEvent("bash", map[string]any{"command": "first"})
	first.Reply = make(chan agent.PermissionReply, 1)
	second := *permEvent("bash", map[string]any{"command": "second"})
	second.Reply = make(chan agent.PermissionReply, 1)
	second.RequestedAt = time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)
	m.handleEvent(first)
	m.handleEvent(second)
	if len(m.permQueue) != 1 || !m.permQueue[0].RequestedAt.Equal(second.RequestedAt) {
		t.Fatal("queued request lost its creation timestamp")
	}
	m.replyPermission(agent.PermissionReply{Allow: true})
	if m.perm == nil || !m.perm.RequestedAt.Equal(second.RequestedAt) {
		t.Fatal("promoting the queued prompt reset its creation timestamp")
	}
	// elapsed uses minutes for old requests. A fresh display-time timer
	// would incorrectly show seconds here instead.
	wait := regexp.MustCompile(`Waiting for your approval · [1-9][0-9]*m [0-9]{2}s`)
	for name, view := range map[string]string{"prompt": m.permissionView(), "live footer": m.liveView()} {
		if !wait.MatchString(plain(view)) {
			t.Errorf("%s lacks the request's original wait age:\n%s", name, plain(view))
		}
	}
}

func TestBackgroundPermissionWaitVisibleWhileForegroundIdle(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 140, 40
	e := *permEvent("bash", map[string]any{"command": "build"})
	e.Agent, e.TaskID = "worker: build", "bg1"
	e.Reply = make(chan agent.PermissionReply, 1)
	e.RequestedAt = time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)
	m.Update(bgEventMsg{e, m.agent})
	if m.running || m.perm == nil || !m.perm.RequestedAt.Equal(e.RequestedAt) {
		t.Fatal("background approval should remain visible without starting a foreground turn")
	}
	for name, view := range map[string]string{"prompt": m.permissionView(), "live footer": m.liveView(), "screen": m.View().Content} {
		v := plain(view)
		if !strings.Contains(v, "Waiting for your approval · ") || strings.Contains(v, "Waiting for the model") {
			t.Errorf("%s hides the idle background approval wait:\n%s", name, v)
		}
	}
	frame := m.motion.frame
	_, cmd := m.Update(m.spin.Tick())
	if cmd == nil || m.motion.frame <= frame {
		t.Fatal("an idle foreground must keep ticking while background approval waits")
	}
}

func TestPermissionWaitWithoutTimestamp(t *testing.T) {
	m := testModel(t)
	m.perm = permEvent("bash", map[string]any{"command": "ls"})
	for name, view := range map[string]string{"prompt": m.permissionView(), "live footer": m.liveView()} {
		v := plain(view)
		if !strings.Contains(v, "Waiting for your approval") || strings.Contains(v, "2562047") || strings.Contains(v, "-1s") {
			t.Errorf("%s mishandles a legacy zero timestamp:\n%s", name, v)
		}
	}
}

func TestPermissionRequestedAtJSON(t *testing.T) {
	at := time.Date(2026, time.October, 24, 12, 34, 56, 123000000, time.UTC)
	e := agent.Event{Kind: agent.EvPermission, RequestedAt: at, Reply: make(chan agent.PermissionReply, 1)}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["requested_at"]) != `"2026-10-24T12:34:56.123Z"` {
		t.Fatalf("requested_at JSON = %s", data)
	}
	var decoded agent.Event
	if err := json.Unmarshal(data, &decoded); err != nil || !decoded.RequestedAt.Equal(at) {
		t.Fatalf("timestamp round trip: %+v, %v", decoded, err)
	}
	var legacy agent.Event
	if err := json.Unmarshal([]byte(`{"type":"permission_request"}`), &legacy); err != nil || !legacy.RequestedAt.IsZero() {
		t.Fatalf("legacy event: %+v, %v", legacy, err)
	}
	data, err = json.Marshal(agent.Event{Kind: agent.EvTextDelta, Text: "hello"})
	if err != nil || strings.Contains(string(data), "requested_at") {
		t.Fatalf("unrelated events should not carry an empty timestamp: %s, %v", data, err)
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

func TestPermissionPromptShowsWhyAutoModeAsks(t *testing.T) {
	m := testModel(t)
	m.perm = permEvent("bash", map[string]any{"command": "git push origin main", "sandbox": false})
	if v := ansi.Strip(m.permissionView()); strings.Contains(v, "auto mode asks") {
		t.Errorf("no reason to show outside auto mode:\n%s", v)
	}
	m.perm.AutoReason = "this pushes to a remote, which the prompt didn't ask for"
	if v := ansi.Strip(m.permissionView()); !strings.Contains(v, "✦ auto mode asks: this pushes to a remote") {
		t.Errorf("the check's reason should be in the prompt:\n%s", v)
	}
	// The mode is in the picker and the shift+tab cycle, and has a label.
	if modeLabels[permission.ModeAuto] == "" || !slices.Contains(modeCycle, permission.ModeAuto) {
		t.Error("auto mode is missing from the labels or the cycle")
	}
}
