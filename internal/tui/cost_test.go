package tui

import (
	"fmt"
	"strings"
	"testing"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/session"
)

func TestRoutingSavingsNote(t *testing.T) {
	// Main model priced: shows what running everything on it would cost.
	got := routingSavingsNote("anthropic/claude-opus-5", llm.Usage{Input: 1_000_000, Output: 1_000_000}, 1.00)
	if !strings.Contains(got, "estimated routing saving $29.0000 (97%)") || !strings.Contains(got, "$30.0000") || !strings.Contains(got, "token use may differ") {
		t.Errorf("note = %q", got)
	}

	// Routing did no better than the main model alone: say what it would
	// have cost, without claiming a saving.
	got = routingSavingsNote("anthropic/claude-opus-5", llm.Usage{Input: 1_000_000}, 5.00)
	if !strings.Contains(got, "would have cost $5.0000") || strings.Contains(got, "saved") {
		t.Errorf("no-saving note = %q", got)
	}

	// The main model itself has no known price: nothing to compare against.
	if got := routingSavingsNote("ollama/qwen3-coder", llm.Usage{Input: 1_000_000}, 0); got != "" {
		t.Errorf("unpriced main model should say nothing, got %q", got)
	}

	// Zero usage: no counterfactual to report.
	if got := routingSavingsNote("anthropic/claude-opus-5", llm.Usage{}, 0); got != "" {
		t.Errorf("no usage should say nothing, got %q", got)
	}
}

func TestCostCommandShowsDelegationOnSameModel(t *testing.T) {
	m := testModel(t)
	m.agent.RecordDelegation()
	m.agent.AddSubagentUsage("m", llm.Usage{Input: 100, Output: 10})
	out := fmt.Sprintf("%v", m.command("/cost")())
	if !strings.Contains(out, "delegated 1 task") || strings.Contains(out, "routing configured · 0") {
		t.Fatalf("/cost lost same-model delegation: %s", out)
	}
}

func TestCompactionCostLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    agent.UsageInfo
		want string
	}{
		{"nothing to report", agent.UsageInfo{}, ""},
		{"measured", agent.UsageInfo{Compactions: 1, CompactionMeasurements: 1, CompactionSavedTokens: 16_000},
			"compacted 1 time · ~16000 tokens of context freed (estimated)"},
		{"partly measured", agent.UsageInfo{Compactions: 3, CompactionMeasurements: 2, CompactionSavedTokens: 25_000},
			"compacted 3 times · ~25000 tokens of context freed (estimated), 2 times measured"},
		{"no usage reported", agent.UsageInfo{Compactions: 2},
			"compacted 2 times · the provider reported no usage for them, so the reduction is unknown"},
		{"the summary came out larger", agent.UsageInfo{Compactions: 1, CompactionMeasurements: 1, CompactionSavedTokens: -500},
			"compacted 1 time · context grew by ~500 tokens"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := compactionCostLine(tc.s); got != tc.want {
				t.Errorf("compactionCostLine =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}
}

// The README sends people to /cost for what the context actually cost, so
// compaction has to show there and not only in the sidebar.
func TestCostCommandReportsCompaction(t *testing.T) {
	m := testModel(t)
	m.agent.Restore(&session.State{Compactions: 2, CompactionMeasurements: 2, CompactionSavedTokens: 30_000})
	out := plain(fmt.Sprintf("%v", m.command("/cost")()))
	if !strings.Contains(out, "compacted 2 times") || !strings.Contains(out, "~30000 tokens of context freed") {
		t.Fatalf("/cost does not report compaction: %s", out)
	}
}

func TestCostCommandShowsSavings(t *testing.T) {
	m := testModel(t) // main model "m", an unpriced test model
	llm.Catalog["m"] = llm.ModelInfo{ID: "m", InputPrice: 5, OutputPrice: 25}
	t.Cleanup(func() { delete(llm.Catalog, "m") })
	llm.Catalog["cheap"] = llm.ModelInfo{ID: "cheap", InputPrice: 0.1, OutputPrice: 0.5}
	t.Cleanup(func() { delete(llm.Catalog, "cheap") })

	m.agent.AddUsage("m", llm.Usage{Input: 1000, Output: 100})
	m.agent.RecordDelegation()
	m.agent.AddSubagentUsage("cheap", llm.Usage{Input: 9000, Output: 900})

	out := fmt.Sprintf("%v", m.command("/cost")())
	if !strings.Contains(out, "cheap") || !strings.Contains(out, "delegated 1 task") || !strings.Contains(out, "estimated routing saving") {
		t.Fatalf("/cost output missing the breakdown or savings note: %s", out)
	}
}
