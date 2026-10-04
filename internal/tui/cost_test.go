package tui

import (
	"fmt"
	"strings"
	"testing"

	"larik/internal/llm"
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
