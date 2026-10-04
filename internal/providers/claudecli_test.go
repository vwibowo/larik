package providers

import (
	"testing"

	"larik/internal/claudeagent"
	"larik/internal/llm"
)

func TestClaudeModelsUseTheResolvedModelForMetadata(t *testing.T) {
	got := claudeModels([]claudeagent.Model{
		// A rolling alias: no catalog lists "sonnet", so only the model it
		// resolves to can say how big its context window is.
		{ID: "sonnet", Desc: "Sonnet 5 · Efficient for routine tasks", Resolved: "claude-sonnet-5",
			Efforts: []string{"low", "medium", "high", "xhigh", "max"}, Thinking: true},
		// A dated release of a catalogued model, which is how the CLI
		// reports its smaller models.
		{ID: "haiku", Desc: "Haiku 4.5 · Fastest for quick answers", Resolved: "claude-haiku-4-5-20251001"},
		// Nothing known about it: better no window than a wrong one.
		{ID: "claude-next-9[1m]", Resolved: "claude-next-9", Efforts: []string{"high", "nonsense"}},
	})
	if len(got) != 3 {
		t.Fatalf("models = %+v", got)
	}
	sonnet, haiku, unknown := got[0], got[1], got[2]
	if sonnet.Context != llm.Catalog["claude-sonnet-5"].ContextWindow || !sonnet.Thinking || !sonnet.CapsKnown {
		t.Errorf("sonnet = %+v", sonnet)
	}
	if len(sonnet.Efforts) != 5 || sonnet.Efforts[4] != llm.EffortMax {
		t.Errorf("sonnet efforts = %v", sonnet.Efforts)
	}
	if sonnet.Desc == "" || !sonnet.Chat || !sonnet.Tools {
		t.Errorf("sonnet picker fields = %+v", sonnet)
	}
	// The window differs sharply between families, which is the whole point
	// of asking: Haiku is 200k where Sonnet is 1M.
	if haiku.Context != llm.Catalog["claude-haiku-4-5"].ContextWindow || haiku.Context == sonnet.Context {
		t.Errorf("haiku context = %d", haiku.Context)
	}
	// Reported as taking no effort levels, so none are offered.
	if len(haiku.Efforts) != 0 || haiku.Thinking {
		t.Errorf("haiku = %+v", haiku)
	}
	if unknown.Context != 0 {
		t.Errorf("unknown model claimed a %d-token window", unknown.Context)
	}
	// An effort level Larik has no constant for is dropped, not passed on.
	if len(unknown.Efforts) != 1 || unknown.Efforts[0] != llm.EffortHigh {
		t.Errorf("unknown efforts = %v", unknown.Efforts)
	}
}

func TestSuggestedClaudeModelsAreTheFallback(t *testing.T) {
	got := suggestedClaudeModels()
	if len(got) != len(claudeagent.SuggestedModels) {
		t.Fatalf("fallback models = %+v", got)
	}
	for _, m := range got {
		// Nothing was reported, so nothing is claimed as known: the picker
		// keeps offering every effort level rather than hiding them.
		if m.CapsKnown || len(m.Efforts) != 5 || !m.Chat || !m.Tools {
			t.Errorf("fallback model %+v should not claim reported capabilities", m)
		}
	}
}
