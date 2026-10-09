package tui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/session"
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

func TestConversationIgnoresHorizontalMouseWheel(t *testing.T) {
	m := testModel(t)
	m.view.SetWidth(12)
	m.view.SetHeight(2)
	m.view.SetContentLines([]string{
		strings.Repeat("a", 40), "second", "third", "fourth",
	})
	m.view.GotoTop()
	for _, event := range []tea.MouseWheelMsg{
		{Button: tea.MouseWheelRight},
		{Button: tea.MouseWheelLeft},
		{Button: tea.MouseWheelDown, Mod: tea.ModShift},
		{Button: tea.MouseWheelUp, Mod: tea.ModShift},
	} {
		m.Update(event)
		if x := m.view.XOffset(); x != 0 {
			t.Fatalf("horizontal wheel %v shifted the conversation by %d", event, x)
		}
	}
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.view.AtTop() {
		t.Fatal("normal wheel should still scroll the conversation vertically")
	}
}

func TestConversationWidthOnlyReservesVisibleSidebar(t *testing.T) {
	m := testModel(t)
	m.setWidth(120)
	m.showInfo = true

	if got, want := m.currentConversationWidth(), 80; got != want {
		t.Fatalf("visible sidebar conversation width = %d, want %d", got, want)
	}
	m.showKeys = true
	if got, want := m.currentConversationWidth(), 120; got != want {
		t.Fatalf("sidebar hidden by shortcuts conversation width = %d, want %d", got, want)
	}
	m.showKeys = false
	m.setWidth(99)
	if got, want := m.currentConversationWidth(), 99; got != want {
		t.Fatalf("narrow info panel conversation width = %d, want %d", got, want)
	}
	m.showInfo = false
	m.setWidth(120)
	if got, want := m.currentConversationWidth(), 120; got != want {
		t.Fatalf("hidden sidebar conversation width = %d, want %d", got, want)
	}
}

func TestMarkdownRerendersWhenSidebarChangesConversationWidth(t *testing.T) {
	m := testModel(t)
	m.setWidth(120)
	m.height = 30
	markdown := "| Component | Description |\n| --- | --- |\n| conversation | A deliberately long description that must be laid out for the available conversation column. |\n\n- A deliberately long list item that should retain markdown indentation when the sidebar opens."
	msg := llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockText, Text: markdown}}}
	cmd := m.handleEvent(agent.Event{Kind: agent.EvAssistant, Message: &msg})
	m.Update(cmd())

	wide := m.outputs[0].text
	if want := m.renderAssistantWidth(msg, 0, 120); wide != want {
		t.Fatal("assistant output was not rendered for the full conversation width")
	}

	m.showInfo = true
	m.View()
	if got, want := m.convWidth, 80; got != want {
		t.Fatalf("conversation width with sidebar = %d, want %d", got, want)
	}
	narrow := m.outputs[0].text
	if narrow == wide {
		t.Fatal("markdown output was not rerendered when the sidebar opened")
	}
	if want := m.renderAssistantWidth(msg, 0, 80); narrow != want {
		t.Fatal("sidebar output was wrapped after rendering instead of being rendered for its column")
	}
	for _, line := range m.convLines {
		if got := lipgloss.Width(line); got > m.convWidth {
			t.Fatalf("sidebar conversation line is %d columns, want at most %d: %q", got, m.convWidth, plain(line))
		}
	}

	m.showInfo = false
	m.View()
	if got, want := m.convWidth, 120; got != want {
		t.Fatalf("conversation width after hiding sidebar = %d, want %d", got, want)
	}
	if got := m.outputs[0].text; got != wide {
		t.Fatal("hiding the sidebar did not restore full-width markdown layout")
	}
}

func TestSidebarStaysAnchoredWithShortConversation(t *testing.T) {
	m := testModel(t)
	m.setWidth(120)
	m.height = 24
	m.showInfo = true
	m.appendOutput("short conversation")

	found := false
	for _, line := range strings.Split(plain(m.View().Content), "\n") {
		if strings.Contains(line, "F2 to hide") {
			found = true
			if lipgloss.Width(line) != m.width {
				t.Fatalf("sidebar should stay anchored at the right edge: width=%d, row=%d, %q", m.width, lipgloss.Width(line), line)
			}
		}
	}
	if !found {
		t.Fatal("rendered sidebar is missing its hide hint")
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
	if m.outputBytes > maxConversationBytes || m.outputs[0].text == "oldest" {
		t.Fatalf("conversation should drop its oldest output past the cap: %d bytes", m.outputBytes)
	}
}

func TestConversationCapAccountsForRerenderedOutput(t *testing.T) {
	m := testModel(t)
	m.setWidth(120)
	expanded := strings.Repeat("x", 1<<16)
	render := func(_ *model, width int) string {
		if width < 100 {
			return expanded
		}
		return "short"
	}
	for range maxConversationBytes/len(expanded) + 2 {
		m.appendRenderedOutput("short", 120, len("source"), render, nil)
	}
	if m.outputBytes >= maxConversationBytes/8 {
		t.Fatalf("wide rendered output unexpectedly large: %d bytes", m.outputBytes)
	}

	m.showInfo = true
	m.View()
	if m.outputBytes > maxConversationBytes {
		t.Fatalf("rerendered output exceeded the conversation cap: %d bytes", m.outputBytes)
	}
	if len(m.outputs) >= maxConversationBytes/len(expanded)+2 {
		t.Fatal("rerendering to a larger layout did not drop the oldest output")
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

func TestClearDeletesScriptState(t *testing.T) {
	m := testModel(t)
	m.agent.Restore(&session.State{CodeState: map[string]json.RawMessage{"cursor": json.RawMessage(`3`)}})
	m.command("/clear")
	path := m.agent.SessionPath()
	st, err := session.Load(path)
	if err != nil || len(st.CodeState) != 0 {
		t.Fatalf("state after /clear = %q, %v", st.CodeState, err)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), `"deleted":["cursor"]`) {
		t.Fatalf("/clear should record the deletion in the session:\n%s", data)
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
