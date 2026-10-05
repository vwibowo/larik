package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"larik/internal/agent"
	"larik/internal/clipboard"
	"larik/internal/llm"
	"larik/internal/mcp"
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

func TestImagePasteInsertsMention(t *testing.T) {
	m := testModel(t)
	typeText(m, "what is this")
	m.Update(imagePastedMsg{path: "/tmp/pastes/paste-1.png"})
	if got := m.input.Value(); got != "what is this @/tmp/pastes/paste-1.png " {
		t.Fatalf("composer = %q", got)
	}
	if out := plain(printed(m.imagePasted(imagePastedMsg{err: clipboard.ErrNoImage}))); !strings.Contains(out, "no image on the clipboard") {
		t.Fatalf("no image: %q", out)
	}
}

func TestExportWritesMarkdown(t *testing.T) {
	m := testModel(t)
	if out := plain(printed(m.command("/export"))); !strings.Contains(out, "nothing to export") {
		t.Fatalf("empty session: %q", out)
	}
	sess := m.agent.SessionPath()
	f, _ := os.OpenFile(sess, os.O_WRONLY|os.O_APPEND, 0o600)
	f.WriteString(`{"type":"message","message":{"role":"user","blocks":[{"type":"text","text":"hello there"}]}}` + "\n")
	f.Close()

	out := plain(printed(m.command("/export notes.md")))
	data, err := os.ReadFile(filepath.Join(m.agent.Cwd(), "notes.md"))
	if err != nil || !strings.Contains(string(data), "hello there") || !strings.Contains(out, "exported 1 message") {
		t.Fatalf("export: %q %v\n%s", out, err, data)
	}
	if out := plain(printed(m.command("/export notes.md"))); !strings.Contains(out, "exists") {
		t.Fatalf("overwrite: %q", out)
	}
}

func TestCopyTracksLastReply(t *testing.T) {
	m := testModel(t)
	if out := plain(printed(m.copyReply())); !strings.Contains(out, "no reply to copy") {
		t.Fatalf("empty: %q", out)
	}
	m.handleEvent(agent.Event{Kind: agent.EvAssistant, Message: &llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{llm.TextBlock("the answer")}}})
	m.handleEvent(agent.Event{Kind: agent.EvAssistant, Agent: "worker: x", Message: &llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{llm.TextBlock("a subagent's")}}})
	if m.lastReply != "the answer" {
		t.Fatalf("lastReply = %q", m.lastReply)
	}
	if lastReply([]llm.Message{llm.UserText("q"), {Role: llm.RoleAssistant, Blocks: []llm.Block{llm.TextBlock("resumed answer")}}}) != "resumed answer" {
		t.Fatal("a resumed session's last reply should be copyable")
	}
}

func TestPermissionQuestionUsesSubagentCwd(t *testing.T) {
	m := testModel(t)
	worktree := t.TempDir()
	os.WriteFile(filepath.Join(worktree, "only-here.go"), []byte("x"), 0o644)
	e := permEvent("write", map[string]any{"path": "only-here.go", "content": "y"})
	if got := m.permQuestion(e); got != "Create this file?" {
		t.Fatalf("project: %q", got)
	}
	e.Cwd = worktree
	if got := m.permQuestion(e); got != "Overwrite this file?" {
		t.Fatalf("in the subagent's worktree the file exists: %q", got)
	}
}

func TestMouseSettingReleasesTheMouse(t *testing.T) {
	m := testModel(t)
	if v := m.View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("mouse scrolling should be on by default: %v", v.MouseMode)
	}
	if _, _, err := m.saveSetting("mouse", "off"); err != nil {
		t.Fatal(err)
	}
	if v := m.View(); v.MouseMode != tea.MouseModeNone {
		t.Fatalf("with mouse scrolling off the terminal should get the mouse: %v", v.MouseMode)
	}
}

func TestMentionPopupOffersResources(t *testing.T) {
	m := testModel(t)
	m.files, m.filesAt = []string{"notes.md"}, time.Now()
	m.resources, m.resourcesAt = []mcp.Resource{
		{Server: "wiki", URI: "page://onboarding", Name: "Onboarding notes"},
		{Server: "wiki", URI: "page://deploy", Name: "Deploy"},
	}, time.Now()
	typeText(m, "see @note")
	var values []string
	for _, it := range m.mention.visible() {
		values = append(values, it.value.(string))
	}
	if strings.Join(values, ",") != "notes.md,wiki:page://onboarding" {
		t.Fatalf("popup: %v", values)
	}
	m.mention.cursor = 1
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.input.Value(); got != "see @wiki:page://onboarding " {
		t.Fatalf("composer = %q", got)
	}
}
