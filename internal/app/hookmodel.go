package app

import (
	"context"
	"errors"
	"strings"

	"larik/internal/agent"
	"larik/internal/hooks"
	"larik/internal/llm"
	"larik/internal/providers"
)

// hookEvaluator answers prompt hooks for ag's session. A hook's own model
// wins; otherwise the explore role, the cheap one for quick reads, when
// it is set; otherwise the session's current model. Spend counts toward
// the session's cost and budget.
func (a *App) hookEvaluator(ag *agent.Agent) hooks.Evaluator {
	return func(ctx context.Context, model, system, prompt string) (string, error) {
		spec := strings.TrimSpace(model)
		if spec == "" {
			if rs, ok := providers.RoleSpec(a.Cfg, "explore"); ok && rs != "" {
				spec = "explore"
			} else {
				spec = ag.ProviderName() + "/" + ag.Model()
			}
		}
		r, err := a.Resolve(a.Cfg, spec)
		if err != nil {
			return "", err
		}
		// A verdict is short, but reasoning models think before it.
		maxOut := llm.Lookup(r.Model).MaxOutput
		if maxOut <= 0 || maxOut > 4096 {
			maxOut = 4096
		}
		req := llm.Request{
			Model:     r.Model,
			System:    system,
			Messages:  []llm.Message{llm.UserText(prompt)},
			MaxTokens: maxOut,
		}
		for ev, err := range r.Provider.Stream(ctx, req) {
			if err != nil {
				return "", err
			}
			if ev.Type == llm.EventDone {
				used := ev.Message.Model
				if used == "" {
					used = r.Model
				}
				ag.AddUsage(used, ev.Usage)
				return ev.Message.Text(), nil
			}
		}
		return "", errors.New("the model sent no answer")
	}
}
