package agent

import (
	"context"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/tools"
)

// probingProvider is a fakeProvider that reports a small window and no tools.
type probingProvider struct {
	*fakeProvider
	window int
	tools  bool
}

func (p *probingProvider) ContextWindow(context.Context, string) int { return p.window }
func (p *probingProvider) SupportsTools(context.Context, string) (bool, bool) {
	return p.tools, true
}

func TestProbeWarnsAndUsesRealWindow(t *testing.T) {
	fp := &probingProvider{fakeProvider: &fakeProvider{script: []llm.Message{
		assistant(llm.TextBlock("one")), assistant(llm.TextBlock("two")),
	}}, window: 105} // fakeProvider reports 100 input tokens per call
	a := New(Options{Provider: fp, Model: "m", Cwd: t.TempDir(), Tools: tools.Default(),
		Perms: permission.NewChecker(permission.ModeYolo, permission.Rules{}, t.TempDir())})

	evs := drain(a.Run(context.Background(), "hi"), PermissionReply{})
	var notices []string
	for _, e := range evs {
		if e.Kind == EvNotice {
			notices = append(notices, e.Text)
		}
	}
	joined := strings.Join(notices, "\n")
	if !strings.Contains(joined, "does not support tool calling") || !strings.Contains(joined, "105-token context window") {
		t.Fatalf("notices = %v", notices)
	}
	if st := a.Stats(); st.ContextWindow != 105 {
		t.Errorf("stats should use the probed window, got %d", st.ContextWindow)
	}

	// Warnings are shown once per model, and a near-empty conversation is
	// never auto-compacted even though it exceeds 80% of the window.
	evs = drain(a.Run(context.Background(), "again"), PermissionReply{})
	for _, e := range evs {
		if e.Kind == EvNotice || e.Kind == EvCompacted {
			t.Errorf("unexpected second-turn event %s: %s", e.Kind, e.Text)
		}
	}
}
