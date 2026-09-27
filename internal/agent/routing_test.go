package agent

import (
	"context"
	"iter"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/tools"
)

// pricedModel adds a catalog entry for the test and removes it after.
func pricedModel(t *testing.T, id string, in, out float64) {
	t.Helper()
	llm.Catalog[id] = llm.ModelInfo{ID: id, ContextWindow: 100_000, MaxOutput: 8_000, InputPrice: in, OutputPrice: out}
	t.Cleanup(func() { delete(llm.Catalog, id) })
}

func TestBudgetWarnsThenStops(t *testing.T) {
	pricedModel(t, "m", 1000, 0) // $0.10 per request of 100 input tokens
	script := []llm.Message{}
	for range 10 {
		script = append(script, assistant(toolUse("t", "glob", `{"pattern":"*"}`)))
	}
	a, _, _ := setup(t, permission.ModeDefault, script...)
	a.opts.Budget = func() (float64, float64) { return 0.25, 0.5 }

	evs := drain(a.Run(context.Background(), "loop"), PermissionReply{Allow: true})

	var notices, errs []string
	var stop string
	for _, e := range evs {
		switch e.Kind {
		case EvNotice:
			notices = append(notices, e.Text)
		case EvError:
			errs = append(errs, e.Text)
		case EvDone:
			stop = e.StopReason
		}
	}
	if stop != "budget" || len(errs) != 1 || !strings.Contains(errs[0], "budget of $0.25 reached ($0.30 spent)") {
		t.Fatalf("stop %q, errors %q", stop, errs)
	}
	warned := 0
	for _, n := range notices {
		if strings.Contains(n, "of the $0.25 session budget spent") {
			warned++
		}
	}
	if warned != 1 {
		t.Errorf("warnings = %d, want 1: %q", warned, notices)
	}
	if got := a.Stats().CostUSD; got < 0.29 || got > 0.31 {
		t.Errorf("cost = %v; stopping should prevent a 4th request", got)
	}
}

func TestSubagentSpendCountsAgainstParentBudget(t *testing.T) {
	pricedModel(t, "m", 1000, 0)
	parent, _, _ := setup(t, permission.ModeDefault)
	parent.opts.Budget = func() (float64, float64) { return 0.15, 0.8 }
	parent.AddUsage("m", llm.Usage{Input: 200}) // $0.20 already spent by another subagent

	child := parent.Spawn(SpawnOptions{Type: "worker", Provider: &fakeProvider{script: []llm.Message{assistant(llm.TextBlock("hi"))}}, Model: "m", Tools: tools.NewRegistry()})
	var stop string
	for e := range child.Run(context.Background(), "go") {
		if e.Kind == EvDone {
			stop = e.StopReason
		}
	}
	if stop != "budget" {
		t.Errorf("child stop = %q, want budget", stop)
	}
}

// switching reports a fallback and answers as another model.
type switching struct{}

func (switching) Name() string { return "primary" }
func (switching) Stream(_ context.Context, req llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		if !yield(llm.StreamEvent{Type: llm.EventNotice, Text: "primary/m failed (HTTP 429); switched to backup/cheap"}, nil) {
			return
		}
		msg := llm.Message{Role: llm.RoleAssistant, Model: "cheap", Blocks: []llm.Block{llm.TextBlock("done")}}
		yield(llm.StreamEvent{Type: llm.EventDone, Message: msg, StopReason: llm.StopEnd, Usage: llm.Usage{Input: 100}}, nil)
	}
}

func TestFallbackNoticeAndUsageByServingModel(t *testing.T) {
	pricedModel(t, "m", 1000, 0)
	pricedModel(t, "cheap", 10, 0)
	a, _, _ := setup(t, permission.ModeDefault)
	a.opts.Provider = switching{}

	evs := drain(a.Run(context.Background(), "hi"), PermissionReply{})
	sawNotice := false
	for _, e := range evs {
		if e.Kind == EvNotice && strings.Contains(e.Text, "switched to backup/cheap") {
			sawNotice = true
		}
		if e.Kind == EvUsage && e.Usage.Model != "cheap" {
			t.Errorf("usage attributed to %q", e.Usage.Model)
		}
	}
	if !sawNotice {
		t.Errorf("fallback notice not shown")
	}
	if got := a.Stats().CostUSD; got != 0.001 {
		t.Errorf("cost = %v, want the cheap model's price", got)
	}
	if sp := a.SpendByModel(); len(sp) != 1 || sp[0].Model != "cheap" {
		t.Errorf("spend by model = %+v", sp)
	}
}

func TestCompactUsesCompactRole(t *testing.T) {
	a, main, _ := setup(t, permission.ModeDefault,
		assistant(llm.TextBlock("one")), assistant(llm.TextBlock("two")))
	drain(a.Run(context.Background(), "first"), PermissionReply{})
	drain(a.Run(context.Background(), "second"), PermissionReply{})

	summarizer := &fakeProvider{script: []llm.Message{{Role: llm.RoleAssistant, Model: "small", Blocks: []llm.Block{llm.TextBlock("<summary>short</summary>")}}}}
	a.opts.CompactWith = func() (llm.Provider, string) { return summarizer, "small" }
	summary, err := a.Compact(context.Background())
	if err != nil || summary != "short" {
		t.Fatalf("summary %q err %v", summary, err)
	}
	if len(summarizer.requests) != 1 || summarizer.requests[0].Model != "small" || len(main.requests) != 2 {
		t.Errorf("compaction went to the wrong model: summarizer %d, main %d", len(summarizer.requests), len(main.requests))
	}
	found := false
	for _, s := range a.SpendByModel() {
		if s.Model == "small" {
			found = true
		}
	}
	if !found {
		t.Errorf("compaction spend not recorded under its model: %+v", a.SpendByModel())
	}
}
