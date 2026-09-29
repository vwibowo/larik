package app

import (
	"context"
	"iter"
	"testing"

	"larik/internal/agent"
	"larik/internal/config"
	"larik/internal/llm"
	"larik/internal/providers"
)

type verdictProvider struct{}

func (verdictProvider) Name() string { return "cheap" }
func (verdictProvider) Stream(_ context.Context, req llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{Type: llm.EventDone, Message: llm.Message{Role: llm.RoleAssistant, Model: req.Model, Blocks: []llm.Block{llm.TextBlock(`{"ok": true}`)}},
			Usage: llm.Usage{Input: 100, Output: 5}}, nil)
	}
}

func TestHookEvaluatorPicksModelAndCountsSpend(t *testing.T) {
	var asked []string
	a := &App{
		Cfg: &config.Config{Providers: map[string]config.ProviderConfig{}, Roles: map[string]string{"explore": "cheap/tiny"}},
		Resolve: func(_ *config.Config, spec string) (providers.Resolved, error) {
			asked = append(asked, spec)
			return providers.Resolved{Provider: verdictProvider{}, Model: "tiny"}, nil
		},
	}
	ag := agent.New(agent.Options{Provider: verdictProvider{}, Model: "main"})
	eval := a.hookEvaluator(ag)

	reply, err := eval(context.Background(), "", "sys", "check")
	if err != nil || reply != `{"ok": true}` {
		t.Fatalf("reply %q %v", reply, err)
	}
	if _, err := eval(context.Background(), "openai/gpt-5.5", "sys", "check"); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 || asked[0] != "explore" || asked[1] != "openai/gpt-5.5" {
		t.Fatalf("models asked: %v", asked)
	}
	if spend := ag.SpendByModel(); len(spend) != 1 || spend[0].Usage.Input != 200 {
		t.Fatalf("hook spend should count toward the session: %+v", spend)
	}

	a.Cfg.Roles = nil // no explore role: the session's model
	eval(context.Background(), "", "sys", "check")
	if asked[2] != "cheap/main" {
		t.Fatalf("without an explore role: %v", asked)
	}
}
