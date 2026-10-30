package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/agent"
)

func sendKey(m *model, code rune, text string) tea.Cmd {
	_, cmd := m.Update(tea.KeyPressMsg{Code: code, Text: text})
	return cmd
}

func bigEdit() *agent.Event {
	var a, b []string
	for i := range 60 {
		a = append(a, fmt.Sprintf("old %d", i))
		b = append(b, fmt.Sprintf("new %d", i))
	}
	return permEvent("edit", map[string]any{"path": "x.txt", "old_string": strings.Join(a, "\n"), "new_string": strings.Join(b, "\n")})
}

func TestApprovalWarnsWhenPreviewIsCut(t *testing.T) {
	m := testModel(t)
	m.setWidth(90)
	m.perm = bigEdit()
	v := plain(m.permissionView())
	for _, want := range []string{"preview is cut off", "d full diff"} {
		if !strings.Contains(v, want) {
			t.Errorf("prompt lacks %q:\n%s", want, v)
		}
	}
	m.perm = permEvent("edit", map[string]any{"path": "x.txt", "old_string": "a", "new_string": "b"})
	if v := plain(m.permissionView()); strings.Contains(v, "cut off") || !strings.Contains(v, "d full diff") {
		t.Errorf("short edit:\n%s", v)
	}
	m.perm = permEvent("edit", map[string]any{"path": "x.txt", "old_string": "a", "new_string": strings.Repeat("long ", 60)})
	if !strings.Contains(plain(m.permissionView()), "cut off") {
		t.Error("a clipped long line should warn too")
	}
	m.perm = permEvent("bash", map[string]any{"command": "ls"})
	if strings.Contains(plain(m.permissionView()), "full diff") {
		t.Error("a command has no diff")
	}
}

func TestDiffViewShowsEveryLineOfAnApproval(t *testing.T) {
	m := testModel(t)
	m.setWidth(90)
	m.height = 24
	m.perm = bigEdit()
	perm := m.perm
	sendKey(m, 'd', "d")
	if m.diffShown() == nil {
		t.Fatal("d did not open the view")
	}
	seen := strings.Builder{}
	for range 40 {
		seen.WriteString(plain(m.diffViewPanel(m.diffShown())))
		sendKey(m, tea.KeyPgDown, "")
	}
	for _, want := range []string{"- old 0", "- old 59", "+ new 0", "+ new 59"} {
		if !strings.Contains(seen.String(), want) {
			t.Errorf("view never showed %q", want)
		}
	}
	sendKey(m, 'y', "y")
	if m.perm != perm || m.diffShown() == nil {
		t.Fatal("y inside the view must not answer the approval")
	}
	sendKey(m, tea.KeyEscape, "")
	if m.perm != perm || m.diffShown() != nil {
		t.Fatal("esc should go back to the prompt, not deny")
	}
}

func TestDiffViewClosesWithItsApproval(t *testing.T) {
	m := testModel(t)
	m.perm = bigEdit()
	m.openPermDiff()
	m.perm = nil
	if m.diffShown() != nil {
		t.Fatal("view outlived its approval")
	}
}

func TestDiffViewWrapsLongLines(t *testing.T) {
	m := testModel(t)
	m.setWidth(60)
	m.height = 30
	long := strings.Repeat("word ", 40) + "end"
	m.perm = permEvent("edit", map[string]any{"path": "x.txt", "old_string": "a", "new_string": long})
	m.openPermDiff()
	if v := plain(m.diffViewPanel(m.diffShown())); !strings.Contains(v, "end") {
		t.Fatalf("the end of a long line is hidden:\n%s", v)
	}
}

func TestSessionEditsCanBePagedThrough(t *testing.T) {
	m := testModel(t)
	m.setWidth(90)
	if sendKey(m, tea.KeyF4, ""); m.diffView != nil {
		t.Fatal("nothing to show yet")
	}
	end := func(input, display string, isErr bool) agent.Event {
		return agent.Event{Kind: agent.EvToolEnd, ToolName: "edit", Input: []byte(input), Display: display, IsError: isErr}
	}
	m.recordDiff(end(`{"path":"a.go"}`, "- a\n+ b", false))
	m.recordDiff(end(`{"path":"failed.go"}`, "- x\n+ y", true))
	m.recordDiff(agent.Event{Kind: agent.EvToolEnd, ToolName: "write", Input: []byte(`{"path":"c.go","content":"new\n"}`)})
	m.recordDiff(agent.Event{Kind: agent.EvToolEnd, ToolName: "bash", Input: []byte(`{}`), Display: "- no"})
	if len(m.diffs) != 2 {
		t.Fatalf("recorded %v", m.diffs)
	}
	sendKey(m, tea.KeyF4, "")
	if v := plain(m.diffViewPanel(m.diffShown())); !strings.Contains(v, "c.go") || !strings.Contains(v, "edit 2 of 2") || !strings.Contains(v, "+ new") {
		t.Fatalf("latest edit:\n%s", v)
	}
	sendKey(m, tea.KeyLeft, "")
	if v := plain(m.diffViewPanel(m.diffShown())); !strings.Contains(v, "a.go") || !strings.Contains(v, "+ b") {
		t.Fatalf("previous edit:\n%s", v)
	}
	if m.composerFocused() {
		t.Fatal("typing must not reach the hidden composer")
	}
	sendKey(m, tea.KeyF4, "")
	if m.diffView != nil {
		t.Fatal("f4 should close it")
	}
}

func TestApprovalTakesOverAnOpenDiffView(t *testing.T) {
	m := testModel(t)
	m.setWidth(90)
	m.diffs = []fileDiff{{path: "a.go", diff: "+ a"}}
	m.showDiffAt(0)
	m.perm = permEvent("bash", map[string]any{"command": "ls"})
	if kind, _ := m.panel(); kind != "permission" {
		t.Fatalf("panel = %q, want the approval on top", kind)
	}
}

func TestRecordedDiffsAreCapped(t *testing.T) {
	m := testModel(t)
	for i := range maxFileDiffs + 5 {
		m.diffs = append(m.diffs, fileDiff{path: fmt.Sprint(i)})
	}
	m.diffs = m.diffs[:maxFileDiffs]
	m.showDiffAt(10)
	m.recordDiff(agent.Event{ToolName: "edit", Input: []byte(`{"path":"z"}`), Display: "+ z"})
	if len(m.diffs) != maxFileDiffs || m.diffView.at != 9 {
		t.Fatalf("len %d, at %d", len(m.diffs), m.diffView.at)
	}
}

func TestDiffViewFitsTheFrame(t *testing.T) {
	m := testModel(t)
	m.setWidth(90)
	m.height = 24
	m.perm = bigEdit()
	sendKey(m, 'd', "d")
	frame := plain(m.View().Content)
	if got := strings.Count(frame, "\n") + 1; got > m.height {
		t.Fatalf("frame is %d rows tall", got)
	}
	for _, want := range []string{"x.txt", "- old 0", "esc back"} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame lacks %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "1. Yes") {
		t.Errorf("the prompt should be hidden behind the view:\n%s", frame)
	}
}
