package tui

import (
	"encoding/json"
	"testing"
	"time"

	"larik/internal/agent"
)

func TestTurnStatsAggregateRequestsAndToolDurations(t *testing.T) {
	m := testModel(t)
	m.handleEvent(agent.Event{Kind: agent.EvUsage, Usage: &agent.UsageInfo{RequestMS: 7200, TTFTMS: 10800}})
	input := json.RawMessage("{\"path\":\"main.go\"}")
	m.handleEvent(agent.Event{Kind: agent.EvToolStart, ToolID: "read-1", ToolName: "read", Input: input})
	m.tools[0].started = time.Now().Add(-2500 * time.Millisecond)
	m.handleEvent(agent.Event{Kind: agent.EvToolEnd, ToolID: "read-1", ToolName: "read", Input: input, Output: "package main"})
	if m.turnStats.Steps != 1 || m.turnStats.ModelTime != 7200*time.Millisecond || m.turnStats.TTFTCount != 1 || m.turnStats.TTFTTotal != 10800*time.Millisecond {
		t.Fatalf("model metrics = %+v", m.turnStats)
	}
	if m.turnStats.ToolTime < 2*time.Second {
		t.Fatalf("tool time = %s", m.turnStats.ToolTime)
	}
}
