package tui

import (
	"strings"
	"testing"

	"larik/internal/agent"
	"larik/internal/permission"
	"larik/internal/status"
)

func TestProgramStatusFollowsTheTurn(t *testing.T) {
	m := testModel(t)
	if got := m.programStatus(); got.State != status.Idle {
		t.Fatalf("new session: %+v", got)
	}

	m.running = true
	if got := m.programStatus(); got.State != status.Working {
		t.Fatalf("running: %+v", got)
	}

	m.perm = &agent.Event{Kind: agent.EvPermission, ToolName: "bash"}
	if got := m.programStatus(); got.State != status.Blocked || got.Kind != status.Permission || !strings.Contains(got.Msg, "bash") {
		t.Fatalf("permission prompt: %+v", got)
	}
	m.perm = &agent.Event{Kind: agent.EvPermission, ToolName: permission.AskUserTool}
	if got := m.programStatus(); got.Kind != status.Question {
		t.Fatalf("question: %+v", got)
	}
	m.perm = nil

	m.Update(runEndedMsg{})
	if got := m.programStatus(); got.State != status.Done {
		t.Fatalf("finished turn: %+v", got)
	}

	m.turnFailed = true
	if got := m.programStatus(); got.State != status.Error {
		t.Fatalf("failed turn: %+v", got)
	}

	m.turnFailed, m.lastStop = false, "interrupted"
	if got := m.programStatus(); got.State != status.Idle {
		t.Fatalf("interrupted turn: %+v", got)
	}
}

func TestReportStatusSendsChangesOnly(t *testing.T) {
	m := testModel(t)
	if m.reportStatus() != nil {
		t.Fatal("idle at startup needs no report")
	}
	m.running = true
	if m.reportStatus() == nil || m.reported.State != status.Working {
		t.Fatalf("a change must be sent: %+v", m.reported)
	}
	if m.reportStatus() != nil {
		t.Fatal("an unchanged status must not be sent again")
	}

	off := false
	m.opts.Config.ProgramStatus = &off
	m.running = false
	if m.reportStatus() != nil {
		t.Fatal("program_status false must send nothing")
	}
}

func TestStatusFileCarriesUsage(t *testing.T) {
	m := testModel(t)
	m.stats.CostUSD, m.stats.ContextTokens, m.stats.ContextWindow = 0.31, 42_000, 100_000
	m.running = true
	m.reportStatus()
	f, ok := status.Read(m.agent.SessionPath())
	if !ok {
		t.Fatal("no status file")
	}
	u := f.Usage
	if u.Model != "m" || u.Provider != "ollama" || u.Mode == "" || u.CostUSD != 0.31 || u.ContextPercent() != 42 {
		t.Fatalf("usage: %+v", u)
	}
}
