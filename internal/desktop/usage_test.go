package desktop

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"larik/internal/session"
	"larik/internal/status"
)

func TestUsageShows(t *testing.T) {
	a := New(Options{})
	busy := session.Indexed{Status: status.Report{State: status.Working}, Running: true,
		Usage: status.Usage{Model: "gpt-6-luna", Mode: "auto", CostUSD: 1.2, Tokens: 86_000, Window: 100_000}}
	busy.ID, busy.Path, busy.Cwd, busy.Title = "s1", "/s1.jsonl", "/work", "Fix the flaky test"
	// A model paid for by subscription has no cost; the default mode says nothing.
	plain := session.Indexed{Status: status.Report{State: status.Idle}, Usage: status.Usage{Model: "codex", Mode: "default", Tokens: 10_000, Window: 200_000}}
	plain.ID, plain.Path, plain.Cwd, plain.Title = "s2", "/s2.jsonl", "/work", "Write the docs"
	a.setProjects([]session.Project{{Root: "/work", Name: "work", Sessions: []session.Indexed{busy, plain}}})
	if got := usageText(busy); got != "gpt-6-luna · 86% context · $1.20 · auto" {
		t.Fatalf("busy: %q", got)
	}
	if got := usageText(plain); got != "codex · 5% context" {
		t.Fatalf("plain: %q", got)
	}
	one := &tab{id: "t1", dir: "/work", path: busy.Path}
	a.tabs = []*tab{one}
	a.activate(one)
	if got := a.where(one); got != "work — working · gpt-6-luna · 86% context · $1.20 · auto" {
		t.Fatalf("the toolbar: %q", got)
	}
	info := a.tabInfo(one)
	if info.Model != "gpt-6-luna" || info.Mode != "auto" || info.CostUSD != 1.2 || info.Context == nil || *info.Context != 86 || info.Tokens != 86_000 {
		t.Fatalf("list: %+v", info)
	}

	a.tabs, a.active = nil, nil // the sidebar alone: this tab has no terminal to draw
	tt := ui.NewTester(a.view, 1240, 800)
	tt.SetDark(true)
	tt.Frame()
	snapshot(t, tt, "usage-dark.png")
	if !tt.HasText("86%") {
		t.Fatalf("no context marker in %q", tt.Texts())
	}
	for _, s := range tt.Texts() {
		if strings.HasPrefix(s, "5%") {
			t.Fatalf("a session with room shows its context: %q", s)
		}
	}
}
