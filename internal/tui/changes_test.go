package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/agent"
)

func changesFixture(t *testing.T) (*model, string) {
	t.Helper()
	m, cwd := rewindFixture(t)
	m.setWidth(90)
	m.height = 30
	m.openChanges()
	if m.changesPick == nil {
		t.Fatal("no changes listed")
	}
	return m, cwd
}

func TestChangesListTurnsAfterResuming(t *testing.T) {
	m, _ := changesFixture(t)
	v := plain(m.changesView())
	for _, want := range []string{"Changes this session", "turn at", "a.txt", "b.txt", "new"} {
		if !strings.Contains(v, want) {
			t.Errorf("panel lacks %q:\n%s", want, v)
		}
	}
	if strings.Count(v, "turn at") != 2 {
		t.Errorf("want two turns:\n%s", v)
	}
	if m.composerFocused() {
		t.Error("typing must not reach the hidden composer")
	}
}

func TestRejectingTheNewestChange(t *testing.T) {
	m, cwd := changesFixture(t)
	path := filepath.Join(cwd, "a.txt")
	before := m.changesPick.rows[1].change
	sendKey(m, tea.KeyEnter, "")
	if v := plain(m.diffViewPanel(m.diffShown())); !strings.Contains(v, "hunk 1 of 1") || !strings.Contains(v, "r reject hunk") {
		t.Fatalf("view:\n%s", v)
	}
	sendKey(m, 'r', "r")
	if v := plain(m.diffViewPanel(m.diffShown())); !strings.Contains(v, "press r again") {
		t.Fatalf("not armed:\n%s", v)
	}
	sendKey(m, tea.KeyDown, "")
	sendKey(m, 'r', "r")
	if text := fileText(t, path); text != string(before.After) {
		t.Fatalf("one r changed the file: %q", text)
	}
	sendKey(m, 'r', "r")
	if text := fileText(t, path); text != string(before.Before) {
		t.Fatalf("file = %q, want %q", text, before.Before)
	}
	if m.diffView != nil {
		t.Fatal("the view should close with its last hunk")
	}
	if m.changesPick == nil {
		t.Fatal("back to the list")
	}
	if v := plain(m.changesView()); strings.Count(v, "turn at") != 1 {
		t.Fatalf("list not refreshed:\n%s", v)
	}
}

func TestAnOlderChangeCannotBeRejected(t *testing.T) {
	m, cwd := changesFixture(t)
	path := filepath.Join(cwd, "a.txt")
	want := fileText(t, path)
	p := m.changesPick
	for p.rows[p.sel].change.Latest && p.sel < len(p.rows)-1 {
		p.move(1)
	}
	if p.rows[p.sel].change.Latest {
		t.Skip("no older change")
	}
	sendKey(m, tea.KeyEnter, "")
	sendKey(m, 'r', "r")
	sendKey(m, 'r', "r")
	if got := fileText(t, path); got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestRejectWaitsForTheTurn(t *testing.T) {
	m, cwd := changesFixture(t)
	path := filepath.Join(cwd, "a.txt")
	want := fileText(t, path)
	sendKey(m, tea.KeyEnter, "")
	m.running = true
	sendKey(m, 'r', "r")
	sendKey(m, 'r', "r")
	if got := fileText(t, path); got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestApprovalLaysOverTheChangesPanel(t *testing.T) {
	m, _ := changesFixture(t)
	m.perm = permEvent("bash", map[string]any{"command": "ls"})
	if kind, _ := m.panel(); kind != "permission" {
		t.Fatalf("panel = %q", kind)
	}
}

func TestWriteApprovalComparesWithTheFile(t *testing.T) {
	m := testModel(t)
	m.setWidth(90)
	path := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.perm = permEvent("write", map[string]any{"path": path, "content": "a\nX\nc\n"})
	v := plain(m.permissionView())
	if !strings.Contains(v, "- b") || !strings.Contains(v, "+ X") || strings.Contains(v, "+ a") {
		t.Fatalf("prompt:\n%s", v)
	}
	m.perm = permEvent("write", map[string]any{"path": path + ".new", "content": "fresh\n"})
	if v := plain(m.permissionView()); !strings.Contains(v, "+ fresh") {
		t.Fatalf("new file:\n%s", v)
	}
}

func TestWriteCardsAndListShowTheDiffFromTheTool(t *testing.T) {
	m := testModel(t)
	m.setWidth(90)
	e := agent.Event{Kind: agent.EvToolEnd, ToolName: "write", Input: []byte(`{"path":"x.txt","content":"a\nnew\n"}`), Display: "  a\n- old\n+ new"}
	if v := plain(m.renderToolCard(e)); !strings.Contains(v, "- old") {
		t.Fatalf("card:\n%s", v)
	}
	m.recordDiff(e)
	if len(m.diffs) != 1 || !strings.Contains(m.diffs[0].diff, "- old") {
		t.Fatalf("diffs = %+v", m.diffs)
	}
}
