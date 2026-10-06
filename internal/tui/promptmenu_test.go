package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/llm"
	"larik/internal/session"
)

// promptMenuFixture is rewindFixture with its history printed and a frame
// laid out, so clicks land on what the screen shows.
func promptMenuFixture(t *testing.T, width, height int) (*model, string) {
	t.Helper()
	m, cwd := rewindFixture(t)
	m.height = height
	m.setWidth(width)
	m.opts.History = m.sess.History
	m.Update(m.printHistory("resumed")())
	m.View()
	return m, cwd
}

// promptRow returns the screen row showing text inside a prompt box, or
// fails when it is scrolled out of view.
func promptRow(t *testing.T, m *model, text string) int {
	t.Helper()
	for i, l := range m.convLines {
		if strings.HasPrefix(strings.TrimSpace(plain(l)), "▌ "+text) {
			y := i - m.view.YOffset()
			if y < 0 || y >= m.viewRows {
				t.Fatalf("prompt %q is on line %d, out of view", text, i)
			}
			return y
		}
	}
	t.Fatalf("no prompt %q in the conversation:\n%s", text, plain(strings.Join(m.convLines, "\n")))
	return 0
}

func click(m *model, x, y int) {
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	m.View()
}

func TestClickingAPromptOpensItsMenu(t *testing.T) {
	m, _ := promptMenuFixture(t, 80, 60)
	click(m, 4, promptRow(t, m, "two"))
	if m.promptMenu == nil || m.promptMenu.ref.n != 2 || m.promptMenu.ref.text != "two" {
		t.Fatalf("clicking prompt 2 should open its menu: %+v", m.promptMenu)
	}
	view := plain(m.View().Content)
	for _, want := range []string{"Prompt 2", "1. Copy", "2. Fork", "3. Rewind"} {
		if !strings.Contains(view, want) {
			t.Errorf("menu should show %q:\n%s", want, view)
		}
	}
	// The box of the chosen prompt changes color, nothing else moves.
	before := len(m.convLines)
	accent, _, _ := strings.Cut(m.st.accent.Render("x"), "x")
	if o := m.outputFor(m.promptMenu.ref); o == nil || !strings.Contains(o.text, accent+"╭") {
		t.Error("the chosen prompt's frame should take the accent color")
	}
	if len(m.convLines) != before {
		t.Error("highlighting must not change the layout")
	}

	// A second click on it closes the menu; a click on a reply doesn't open one.
	click(m, 4, promptRow(t, m, "two"))
	if m.promptMenu != nil {
		t.Fatal("a second click should close the menu")
	}
	for i, l := range m.convLines {
		if strings.TrimSpace(plain(l)) == "ok" {
			click(m, 4, i-m.view.YOffset())
			break
		}
	}
	if m.promptMenu != nil {
		t.Error("a reply is not a prompt")
	}
	// Nor does a click right of the conversation.
	click(m, m.viewWidth+1, promptRow(t, m, "two"))
	if m.promptMenu != nil {
		t.Error("a click beside the conversation should do nothing")
	}
}

func TestPromptClicksFollowScrollAndResize(t *testing.T) {
	m, _ := promptMenuFixture(t, 80, 14) // too short for everything
	m.view.GotoTop()
	m.View()
	click(m, 4, promptRow(t, m, "one"))
	if m.promptMenu == nil || m.promptMenu.ref.n != 1 {
		t.Fatalf("scrolled to the top, the click should hit prompt 1: %+v", m.promptMenu)
	}
	m.closePromptMenu()

	m.setWidth(40)
	m.height = 60
	m.View()
	click(m, 4, promptRow(t, m, "three"))
	if m.promptMenu == nil || m.promptMenu.ref.n != 3 || m.promptMenu.ref.text != "three" {
		t.Fatalf("after a resize the click should hit prompt 3 without its note: %+v", m.promptMenu)
	}
}

func TestPromptMenuFork(t *testing.T) {
	m, _ := promptMenuFixture(t, 80, 60)
	original := m.sess.ID
	click(m, 4, promptRow(t, m, "two"))
	m.Update(tea.KeyPressMsg{Code: '2', Text: "2"})
	if m.promptMenu != nil || m.sess.ID == original {
		t.Fatal("Fork should branch into a new session")
	}
	st, err := session.Load(m.sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.All) != 4 || session.PromptText(st.All[2]) != "two" || st.All[3].Role != llm.RoleAssistant {
		t.Errorf("the branch should keep prompt 2 and its reply, and nothing after: %d messages", len(st.All))
	}
}

func TestPromptMenuRewind(t *testing.T) {
	m, cwd := promptMenuFixture(t, 80, 60)
	original := m.sess.ID
	click(m, 4, promptRow(t, m, "three"))
	m.Update(tea.KeyPressMsg{Code: '3', Text: "3"})
	if m.rewindOffer == nil || m.sess.ID != original {
		t.Fatal("Rewind should ask about the changed files, as /rewind does")
	}
	m.Update(tea.KeyPressMsg{Code: '2', Text: "2"}) // branch only
	if m.sess.ID == original || m.input.Value() != "three" {
		t.Errorf("Rewind should branch and put the prompt back without its note, got %q", m.input.Value())
	}
	if fileText(t, filepath.Join(cwd, "a.txt")) != "a3" {
		t.Error("'Branch only' leaves the files")
	}
}

func TestPromptMenuWaitsForTheTurn(t *testing.T) {
	m, _ := promptMenuFixture(t, 80, 60)
	original := m.sess.ID
	click(m, 4, promptRow(t, m, "two"))
	m.running = true
	cmd := m.handlePromptMenuKey(tea.KeyPressMsg{Code: '2', Text: "2"})
	m.running = false
	if m.sess.ID != original {
		t.Fatal("Fork must wait for the running turn")
	}
	if out, ok := cmd().(outputMsg); !ok || !strings.Contains(plain(string(out)), "Fork is unavailable while a turn is running") {
		t.Errorf("Fork should say why it can't run: %v", cmd())
	}
	// Copy needs no session, so it works at any time.
	click(m, 4, promptRow(t, m, "two"))
	m.running = true
	if cmd := m.runPromptAction(promptCopy); cmd == nil || m.promptMenu != nil {
		t.Error("Copy should run and close the menu")
	}
}

func TestPromptMenuClickPicksAnItem(t *testing.T) {
	m, _ := promptMenuFixture(t, 80, 60)
	original := m.sess.ID
	click(m, 4, promptRow(t, m, "two"))
	lines := strings.Split(m.promptMenuView(), "\n")
	for i, l := range lines {
		if strings.Contains(plain(l), "2. Fork") {
			click(m, 6, m.panelTop+i)
			break
		}
	}
	if m.sess.ID == original {
		t.Fatal("clicking Fork in the menu should branch")
	}
}

func TestPromptCommandOpensTheMenu(t *testing.T) {
	m, _ := promptMenuFixture(t, 80, 60)
	m.command("/prompt 1")
	if m.promptMenu == nil || m.promptMenu.ref.n != 1 || m.outputFor(m.promptMenu.ref) == nil {
		t.Fatalf("/prompt 1 should open the menu on prompt 1 as shown: %+v", m.promptMenu)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.promptMenu != nil {
		t.Fatal("esc closes the menu")
	}
	m.command("/prompt")
	if m.promptMenu == nil || m.promptMenu.ref.n != 3 || m.promptMenu.ref.text != "three" {
		t.Errorf("/prompt alone should pick the last prompt: %+v", m.promptMenu)
	}
	m.closePromptMenu()
	m.command("/prompt 9")
	if m.promptMenu != nil {
		t.Error("an out-of-range prompt is an error")
	}
}

func TestRewindListHidesSystemNotes(t *testing.T) {
	m, _ := rewindFixture(t)
	out, ok := m.command("/rewind")().(outputMsg)
	if !ok || strings.Contains(string(out), "system-note") || !strings.Contains(plain(string(out)), "3  three") {
		t.Errorf("/rewind should list prompts as typed:\n%s", out)
	}
	m.command("/rewind 3 keep")
	if m.input.Value() != "three" {
		t.Errorf("/rewind should put back the prompt without its note, got %q", m.input.Value())
	}
}

func TestHistoryOnlyNumbersRealPrompts(t *testing.T) {
	chunks, count := historyChunks([]llm.Message{
		llm.UserText("first"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{llm.TextBlock("a")}},
		llm.UserText(`<task-notification id="x">done</task-notification>`),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{llm.TextBlock("b")}},
		llm.UserText("second"),
	}, 0)
	if count != 2 {
		t.Fatalf("two prompts, got %d", count)
	}
	var refs []string
	for _, c := range chunks {
		if c.prompt != nil {
			refs = append(refs, c.prompt.text)
		}
	}
	if strings.Join(refs, ",") != "first,second" || len(chunks) != 3 {
		t.Errorf("each prompt is its own chunk and notifications aren't prompts: %v in %d chunks", refs, len(chunks))
	}
}

func TestFindPromptFallsBackToText(t *testing.T) {
	st := &session.State{All: []llm.Message{llm.UserText("a"), llm.UserText("b"), llm.UserText("a")}}
	prompts := session.Prompts(st.All)
	for _, c := range []struct {
		ref  promptRef
		want int
	}{
		{promptRef{n: 2, text: "b"}, 2},
		{promptRef{n: 9, text: "b"}, 2}, // numbering drifted
		{promptRef{n: 2, text: "a"}, 1}, // nearest; ties go to the earlier
		{promptRef{n: 3, text: "a"}, 3},
		{promptRef{n: 1, text: "gone"}, 0},
	} {
		if got := findPrompt(st, prompts, &c.ref); got != c.want {
			t.Errorf("findPrompt(%+v) = %d, want %d", c.ref, got, c.want)
		}
	}
}
