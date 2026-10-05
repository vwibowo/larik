package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"larik/internal/agent"
)

func TestCompactionRefreshesContextAndTurnStats(t *testing.T) {
	m := testModel(t)
	m.stats.ContextTokens = 26_000
	info := agent.CompactionInfo{BeforeTokens: 26_000, AfterTokens: 10_000, SavedTokens: 16_000, Estimated: true, Available: true}
	got := plain(printed(m.handleEvent(agent.Event{Kind: agent.EvCompacted, Compaction: &info})))
	if m.stats.ContextTokens != 0 {
		t.Fatalf("context after compaction = %d, want refreshed unknown/zero", m.stats.ContextTokens)
	}
	if m.turnStats.Compactions != 1 || m.turnStats.CompactionMeasurements != 1 || m.turnStats.CompactionSavedTokens != 16_000 {
		t.Fatalf("turn compaction stats: %+v", m.turnStats)
	}
	for _, want := range []string{"26k prompt", "~10k summary", "~16k saved"} {
		if !strings.Contains(got, want) {
			t.Fatalf("compaction line lacks %q: %q", want, got)
		}
	}
}

func TestManualCompactionUpdatesTurnStats(t *testing.T) {
	m := testModel(t)
	info := agent.CompactionInfo{BeforeTokens: 20_000, AfterTokens: 5_000, SavedTokens: 15_000, Estimated: true, Available: true}
	m.update(compactedMsg{summary: "short", compaction: info})
	if m.turnStats.Compactions != 1 || m.turnStats.CompactionMeasurements != 1 || m.turnStats.CompactionSavedTokens != 15_000 {
		t.Fatalf("manual compaction turn stats: %+v", m.turnStats)
	}
}

// A subagent compacts its own context, which is not this one. Counting it
// in the turn's compression figures would misreport both: the main context
// never shrank, and the child's saving isn't the parent's.
func TestASubagentsCompactionIsLabelledAndNotTheTurnsOwn(t *testing.T) {
	m := testModel(t)
	m.stats.ContextTokens = 50_000
	info := agent.CompactionInfo{BeforeTokens: 30_000, AfterTokens: 6_000, SavedTokens: 24_000, Estimated: true, Available: true}
	got := plain(printed(m.handleEvent(agent.Event{
		Kind: agent.EvCompacted, Agent: "explore: find auth", Model: "haiku-4-5", Compaction: &info,
	})))

	if m.turnStats.Compactions != 0 || m.turnStats.CompactionSavedTokens != 0 {
		t.Errorf("a child's compaction counted as the turn's: %+v", m.turnStats)
	}
	if m.stats.ContextTokens != 50_000 {
		t.Errorf("the main context reading changed to %d; the child compacted, not this context", m.stats.ContextTokens)
	}
	for _, want := range []string{"explore: find auth", "30k prompt", "~24k saved"} {
		if !strings.Contains(got, want) {
			t.Errorf("subagent compaction line lacks %q: %q", want, got)
		}
	}
}

// A child's notice and error used to render unlabelled, so a subagent's
// "auto-compaction failed" read as the main conversation's own failure.
func TestASubagentsNoticeAndErrorNameIt(t *testing.T) {
	m := testModel(t)
	notice := plain(printed(m.handleEvent(agent.Event{Kind: agent.EvNotice, Agent: "worker: fix it", Text: "auto-compaction failed: no summarizer"})))
	if !strings.Contains(notice, "worker: fix it") {
		t.Errorf("notice should name the subagent it came from: %q", notice)
	}
	failed := plain(printed(m.handleEvent(agent.Event{Kind: agent.EvError, Agent: "worker: fix it", Text: "boom"})))
	if !strings.Contains(failed, "worker: fix it") {
		t.Errorf("error should name the subagent it came from: %q", failed)
	}
	// The main agent's own messages stay unadorned.
	own := plain(printed(m.handleEvent(agent.Event{Kind: agent.EvNotice, Text: "a plain notice"})))
	if strings.Contains(own, "↳") {
		t.Errorf("the main agent's notice should not be nested: %q", own)
	}
}

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
