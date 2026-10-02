package agent

import (
	"context"
	"fmt"

	"larik/internal/llm"
)

// truncationWarnAt is the share of the context window at which a request
// likely got (or is about to get) truncated by the server.
const truncationWarnAt = 0.9

// reprobeAt is the share of a known window past which it is checked again.
const reprobeAt = 0.5

// windowLocked is the context window for the current model: the one the
// provider reported, else the catalog's. Callers hold a.mu.
func (a *Agent) windowLocked() int {
	if w := a.windows[a.opts.Model]; w > 0 {
		return w
	}
	return llm.Lookup(a.opts.Model).ContextWindow
}

// checkTools warns once per model when the provider knows the model can't
// call tools, since the agent can't work without them.
func (a *Agent) checkTools(ctx context.Context, p llm.Provider, model string, needTools bool, emit func(Event)) {
	prober, ok := p.(llm.ModelProber)
	if !ok || !needTools {
		return
	}
	a.mu.Lock()
	if a.probed == nil {
		a.probed = map[string]bool{}
	}
	done := a.probed[model]
	a.probed[model] = true
	a.mu.Unlock()
	if done {
		return
	}
	if supported, known := prober.SupportsTools(ctx, model); known && !supported {
		emit(Event{Kind: EvNotice, Text: fmt.Sprintf("%s does not support tool calling on %s, so it can't read, edit or run anything; pick a tool-capable model with /model", model, p.Name())})
	}
}

// probeWindow records the context window the provider actually runs the
// model with, and warns when a request filled it.
func (a *Agent) probeWindow(ctx context.Context, p llm.Provider, model string, u llm.Usage, emit func(Event)) {
	prober, ok := p.(llm.ModelProber)
	if !ok {
		return
	}
	// Asking costs a request to the server; once the window is known, ask
	// again only when this request came near it, the one case where a
	// changed window (a model reloaded with another size) matters.
	used := u.ContextTokens()
	a.mu.Lock()
	known := a.windows[model]
	a.mu.Unlock()
	if known > 0 && float64(used) < reprobeAt*float64(known) {
		return
	}
	w := prober.ContextWindow(ctx, model)
	if w <= 0 {
		return
	}
	a.mu.Lock()
	if a.windows == nil {
		a.windows, a.warned = map[string]int{}, map[string]bool{}
	}
	a.windows[model] = w
	warn := float64(used) >= truncationWarnAt*float64(w) && !a.warned[model]
	if warn {
		a.warned[model] = true
	}
	a.mu.Unlock()
	if !warn {
		return
	}
	msg := fmt.Sprintf("%s is running %s with a %d-token context window and this request used %d tokens; "+
		"the server likely truncated the prompt, dropping instructions or tool definitions.", p.Name(), model, w, used)
	if p.Name() == "ollama" {
		msg += ` Raise it with "context_length" under providers.ollama in ~/.config/larik/config.json, e.g. 65536.`
	}
	emit(Event{Kind: EvNotice, Text: msg})
}
