package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"larik/internal/agent"
)

func TestConversationWrapsOnceAndRewrapsOnResize(t *testing.T) {
	m := testModel(t)
	m.setWidth(40)
	m.Update(outputMsg(strings.Repeat("word ", 30)))
	narrow := len(m.convLines)
	for _, l := range m.convLines {
		if w := lipgloss.Width(l); w > 40 {
			t.Fatalf("line wider than the terminal (%d): %q", w, l)
		}
	}
	m.setWidth(120)
	if len(m.convLines) >= narrow {
		t.Fatalf("resize should re-wrap: %d lines at 40, %d at 120", narrow, len(m.convLines))
	}
}

func TestSidebarKeepsHeightWhileResponseStreams(t *testing.T) {
	m := testModel(t)
	m.setWidth(120)
	m.height = 24
	m.showInfo = true
	m.appendOutput(strings.Repeat("history line\n", 50))

	plainLines := func() []string {
		return strings.Split(plain(m.View().Content), "\n")
	}
	before := plainLines()
	if len(before) != m.height {
		t.Fatalf("idle view has %d rows, want %d", len(before), m.height)
	}
	var hintRow int
	for i, line := range before {
		if strings.Contains(line, "F2 to hide") {
			hintRow = i
		}
	}

	m.running = true
	m.stream.WriteString(strings.Repeat("long response ", 1000))
	after := plainLines()
	if len(after) != m.height {
		t.Fatalf("streaming view has %d rows, want %d", len(after), m.height)
	}
	streamHintRow := -1
	for i, line := range after {
		if strings.Contains(line, "F2 to hide") {
			streamHintRow = i
		}
		if w := lipgloss.Width(line); w > m.width {
			t.Fatalf("streaming row exceeds terminal width (%d > %d): %q", w, m.width, line)
		}
	}
	if streamHintRow != hintRow {
		t.Fatalf("sidebar height changed while streaming: hint row %d -> %d", hintRow, streamHintRow)
	}
	if !strings.Contains(strings.Join(after, "\n"), "long response") {
		t.Fatal("streaming response is missing from the conversation column")
	}
}

func TestConversationDropsOldestPastCap(t *testing.T) {
	m := testModel(t)
	m.setWidth(80)
	m.Update(outputMsg("oldest"))
	chunk := strings.Repeat("x", 1<<16)
	for range maxConversationBytes/len(chunk) + 1 {
		m.Update(outputMsg(chunk))
	}
	if m.outputBytes > maxConversationBytes || m.outputs[0] == "oldest" {
		t.Fatalf("conversation should drop its oldest output past the cap: %d bytes", m.outputBytes)
	}
}

func TestClearResetsTheTranscriptView(t *testing.T) {
	m := testModel(t)
	m.setWidth(80)
	m.height = 24
	m.Update(outputMsg("an old reply"))
	divider := printed(m.command("/clear"))
	if m.outputs != nil || m.outputBytes != 0 || m.convLines != nil {
		t.Fatalf("/clear should drop the printed conversation: %d outputs, %d bytes, %d lines", len(m.outputs), m.outputBytes, len(m.convLines))
	}
	m.Update(outputMsg(divider))
	view := plain(m.View().Content)
	if strings.Contains(view, "an old reply") {
		t.Fatalf("the old transcript should be gone after /clear:\n%s", view)
	}
	if !strings.Contains(view, "context cleared") {
		t.Fatalf("/clear should print its divider:\n%s", view)
	}
}

func TestPermissionPromptKeepsConversationVisible(t *testing.T) {
	m := testModel(t)
	m.setWidth(80)
	m.height = 24
	m.Update(outputMsg("I will list the files first"))
	m.perm = &agent.Event{Kind: agent.EvPermission, ToolName: "bash", Input: []byte(`{"command":"ls"}`)}
	view := plain(m.View().Content)
	if !strings.Contains(view, "I will list the files first") || !strings.Contains(view, "Yes") {
		t.Fatalf("prompt should show above the conversation it belongs to: %q", view)
	}
	if got := len(strings.Split(view, "\n")); got != m.height {
		t.Fatalf("view has %d rows, want %d", got, m.height)
	}
}

func TestLiveViewSitsBelowConversation(t *testing.T) {
	m := testModel(t)
	m.setWidth(80)
	m.height = 20
	m.Update(outputMsg("earlier answer"))
	m.running = true
	m.stream.WriteString("streaming answer")
	view := plain(m.View().Content)
	if i, j := strings.Index(view, "earlier answer"), strings.Index(view, "streaming answer"); i < 0 || j < i {
		t.Fatalf("live text should follow the conversation: %q", view)
	}
	checkPinnedView(t, m)
}

func TestCtrlHomeMovesWithinInputWhenTyping(t *testing.T) {
	m := testModel(t)
	m.setWidth(80)
	m.height = 18
	for i := range 40 {
		m.Update(outputMsg(strings.Repeat("line ", 3) + string(rune('a'+i%26))))
	}
	m.View()
	m.input.SetValue("first\nsecond")
	m.Update(tea.KeyPressMsg{Code: tea.KeyHome, Mod: tea.ModCtrl})
	if m.input.Line() != 0 || !m.view.AtBottom() {
		t.Fatalf("ctrl+home with text should move the cursor, not scroll: line %d", m.input.Line())
	}
	m.input.Reset()
	m.Update(tea.KeyPressMsg{Code: tea.KeyHome, Mod: tea.ModCtrl})
	if !m.view.AtTop() {
		t.Fatal("ctrl+home with an empty input should scroll to the top")
	}
}

func TestSessionPickerWithoutAppPrintsResumeHint(t *testing.T) {
	m := testModel(t)
	m.sessionPick = &picker{items: []pickItem{{label: "abc", value: "abc"}}}
	m.sessionPick.home()
	cmd := m.handleSessionPickerKey(press(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("expected a hint")
	}
	if out, ok := cmd().(outputMsg); !ok || !strings.Contains(plain(string(out)), "larik --resume abc") {
		t.Fatalf("hint: %v", out)
	}
}

func TestPickerMatchesDetailOnlyWhenAsked(t *testing.T) {
	p := &picker{filterable: true, items: []pickItem{{label: "ollama/m", detail: "128k ctx"}}}
	p.filter = "128k"
	if len(p.visible()) != 0 {
		t.Fatal("details should not match by default")
	}
	p.matchDetail = true
	if len(p.visible()) != 1 {
		t.Fatal("matchDetail should search details")
	}
}

func TestHighlightDiffKeepsBlocksTogether(t *testing.T) {
	m := testModel(t)
	m.setWidth(80)
	diff := "- /* old\n- comment */\n+ x := `a\n+ b`"
	out := m.highlightDiff("main.go", diff, 10)
	if plain(out) != diff {
		t.Fatalf("highlight changed diff text: %q", plain(out))
	}
	// The second line continues the comment, so it must be colored as one
	// rather than lexed on its own as plain identifiers.
	lines, ok := m.colorLines("main.go", "/* old\ncomment */", lipgloss.NewStyle())
	if !ok || len(lines) != 2 || !strings.Contains(lines[1], "\x1b[") || plain(lines[1]) != "comment */" {
		t.Fatalf("multi-line comment: %q", lines)
	}
	if got := plain(m.highlightDiff("notes.unknown", "+ added\n  context", 10)); got != "+ added\n  context" {
		t.Fatalf("fallback: %q", got)
	}
}
