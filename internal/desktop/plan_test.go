package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/session"
	"larik/internal/status"
)

func TestLatestPlanFromTranscript(t *testing.T) {
	s, err := session.Create(t.TempDir(), session.Meta{})
	if err != nil {
		t.Fatal(err)
	}
	for _, plan := range []string{"## First plan\nDo A", "## Revised plan\nDo B"} {
		input, _ := json.Marshal(map[string]string{"plan": plan})
		if err := s.AppendMessage(llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, Name: permission.ExitPlanTool, Input: input}}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	got, err := latestPlan(s.Path)
	if err != nil || got != "## Revised plan\nDo B" {
		t.Fatalf("latest plan: %q, %v", got, err)
	}
}

func TestRenderedPlanRequestsChanges(t *testing.T) {
	needTerminal(t)
	data, repo := fixture(t)
	bin := filepath.Join(t.TempDir(), "prompt")
	script := "#!/bin/sh\nstty raw -echo\nk=$(dd bs=1 count=1 2>/dev/null)\nstty sane\necho \"got:$k\"\nsleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := session.Create(session.Dir(data, repo), session.Meta{Cwd: repo})
	if err != nil {
		t.Fatal(err)
	}
	s.AppendMessage(llm.UserText("Build the feature"), nil)
	s.AppendMessage(llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, Name: permission.ExitPlanTool, Input: json.RawMessage(`{"plan":"## Build the feature\n1. Add the pane\n2. Run tests"}`)}}}, nil)
	s.Close()
	status.NewWriter(s.Path, "tui").Set(status.Report{State: status.Blocked, Kind: status.Permission, Msg: status.PlanReview})
	a, mu := serialApp(Options{Bin: bin, DataDir: data, Cwd: repo})
	defer a.Close()
	mu.Lock()
	tb := a.startTab(repo, "", s.Path)
	a.activate(tb)
	a.setProjects(index(t, data))
	mu.Unlock()
	waitFor(t, mu, "plan preview", func() bool { p := a.plans[s.Path]; return p != nil && !p.loading })
	mu.Lock()
	view := ui.NewTester(a.view, 1240, 800)
	view.Frame()
	for _, want := range []string{"Review plan", "Build the feature", "Add the pane", "Approve and accept edits", "Approve, ask each edit", "Request changes"} {
		if !view.HasText(want) {
			mu.Unlock()
			t.Fatalf("plan pane missing %q: %q", want, view.Texts())
		}
	}
	snapshot(t, view, "review-plan.png")
	if err := view.Click("Request changes"); err != nil {
		mu.Unlock()
		t.Fatal(err)
	}
	if !strings.Contains(a.notice, "Describe the changes") || len(a.answered) != 0 {
		mu.Unlock()
		t.Fatalf("feedback prompt: notice %q, pending %v", a.notice, a.answered)
	}
	mu.Unlock()
	frameUntil(t, mu, view, "request changes key", func() bool { return strings.Contains(tb.term.Text(), "got:n") })
}

func TestRenderedPlanApprovals(t *testing.T) {
	needTerminal(t)
	for _, tc := range []struct {
		label string
		key   string
	}{
		{"Approve and accept edits", "1"},
		{"Approve, ask each edit", "2"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			data, repo := fixture(t)
			bin := filepath.Join(t.TempDir(), "prompt")
			script := "#!/bin/sh\nstty raw -echo\nk=$(dd bs=1 count=1 2>/dev/null)\nstty sane\necho \"got:$k\"\nsleep 30\n"
			if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			s, err := session.Create(session.Dir(data, repo), session.Meta{Cwd: repo})
			if err != nil {
				t.Fatal(err)
			}
			s.AppendMessage(llm.UserText("Build the feature"), nil)
			s.AppendMessage(llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, Name: permission.ExitPlanTool, Input: json.RawMessage(`{"plan":"## Build the feature"}`)}}}, nil)
			s.Close()
			status.NewWriter(s.Path, "tui").Set(status.Report{State: status.Blocked, Kind: status.Permission, Msg: status.PlanReview})
			a, mu := serialApp(Options{Bin: bin, DataDir: data, Cwd: repo})
			defer a.Close()
			mu.Lock()
			tb := a.startTab(repo, "", s.Path)
			a.activate(tb)
			a.setProjects(index(t, data))
			mu.Unlock()
			waitFor(t, mu, "plan preview", func() bool { p := a.plans[s.Path]; return p != nil && !p.loading })
			mu.Lock()
			view := ui.NewTester(a.view, 1240, 800)
			view.Frame()
			if err := view.Click(tc.label); err != nil {
				mu.Unlock()
				t.Fatal(err)
			}
			mu.Unlock()
			frameUntil(t, mu, view, "plan approval key", func() bool { return strings.Contains(tb.term.Text(), "got:"+tc.key) })
		})
	}
}
