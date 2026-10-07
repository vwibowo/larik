package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"larik/internal/llm"
	"larik/internal/trace"
)

// suggestMaxOutput bounds a prompt suggestion. The answer is a few words,
// but a model that thinks spends some of this before it.
const (
	suggestMaxOutput = 1024
	suggestMaxChars  = 150
)

// SuggestNext predicts the user's next prompt from the conversation so
// far, for a front end to offer as ghost text. It asks the agent's own
// model with the conversation's system prompt, tools and cache key, so
// the request reuses the cached prefix and costs little more than the
// question and a short reply. Nothing is added to the transcript; the
// spend is recorded like a compaction's, without changing the measured
// context. It returns "" when there is nothing worth suggesting.
func (a *Agent) SuggestNext(ctx context.Context) (string, error) {
	if !a.underBudget() {
		return "", nil
	}
	a.mu.Lock()
	if a.opts.Runtime != nil || a.opts.Provider == nil || !endsWithReply(a.messages) {
		a.mu.Unlock()
		return "", nil
	}
	msgs := append([]llm.Message(nil), a.messages...)
	req := llm.Request{
		Model:     a.opts.Model,
		System:    a.opts.System,
		Tools:     a.activeLocked().Specs(), // unchanged tools keep the cached prefix valid
		MaxTokens: suggestMaxOutput,
		Effort:    a.opts.Effort,
		CacheKey:  a.cacheKey,
	}
	provider := a.opts.Provider
	if a.opts.SamplingFor != nil {
		req.Sampling = a.opts.SamplingFor(provider.Name(), req.Model)
	}
	tr := a.opts.Trace
	a.mu.Unlock()
	if maxOutput := llm.Lookup(req.Model).MaxOutput; maxOutput > 0 {
		req.MaxTokens = min(req.MaxTokens, maxOutput)
	}
	req.Messages = withUserText(msgs, suggestionPrompt)

	reqID := tr.Request(provider.Name(), req, "suggestion")
	ctx = tr.Wire(ctx, reqID)
	start := time.Now()
	var final llm.Message
	done := false
	for ev, err := range provider.Stream(ctx, req) {
		if err != nil {
			tr.Response(reqID, trace.Response{Start: start.UnixMilli(), DurationMS: time.Since(start).Milliseconds(), Error: err.Error()})
			return "", err
		}
		if ev.Type != llm.EventDone {
			continue
		}
		final, done = ev.Message, true
		usage := ev.Usage
		tr.Response(reqID, trace.Response{Message: &final, Usage: &usage, CostUSD: llm.Lookup(req.Model).Cost(usage),
			StopReason: string(ev.StopReason), Start: start.UnixMilli(), DurationMS: time.Since(start).Milliseconds()})
		model := final.Model
		if model == "" {
			model = req.Model
		}
		a.recordSideUsage(model, usage)
	}
	if !done {
		return "", errors.New("no reply")
	}
	return parseSuggestion(final.Text()), nil
}

// endsWithReply reports whether the conversation ends with the model's
// answer: an assistant message with text and no pending tool call.
func endsWithReply(msgs []llm.Message) bool {
	if len(msgs) == 0 {
		return false
	}
	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleAssistant {
		return false
	}
	for _, b := range last.Blocks {
		if b.Type == llm.BlockToolUse {
			return false
		}
	}
	return strings.TrimSpace(last.Text()) != ""
}

// recordSideUsage adds the spend of a request that isn't part of the
// conversation. Unlike recordUsage it leaves lastContext alone: the
// request carried an extra question, and its reply isn't kept.
func (a *Agent) recordSideUsage(model string, u llm.Usage) {
	a.mu.Lock()
	a.usage.Add(u)
	a.cost += llm.Lookup(model).Cost(u)
	a.addModelUsageLocked(model, u)
	a.mu.Unlock()
	if a.opts.Session != nil {
		a.saveFailed(a.opts.Session.AppendUsage(model, u))
	}
}

// underBudget reports whether the session's caps leave room for another
// request, without the warnings checkBudget gives along the way.
func (a *Agent) underBudget() bool {
	r := a.root()
	var capUSD float64
	var capTokens int64
	if r.opts.Budget != nil {
		capUSD, _ = r.opts.Budget()
	}
	if r.opts.TokenBudget != nil {
		capTokens, _ = r.opts.TokenBudget()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if capUSD > 0 && r.cost >= capUSD {
		return false
	}
	used := int64(r.usage.Input + r.usage.Output + r.usage.CacheRead + r.usage.CacheWrite)
	return capTokens <= 0 || used < capTokens
}

// parseSuggestion turns the model's answer into a prompt to offer, or ""
// when it declined or answered with something that isn't one.
func parseSuggestion(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	for _, label := range []string{"User:", "user:", "Suggestion:", "suggestion:"} {
		s = strings.TrimSpace(strings.TrimPrefix(s, label))
	}
	for _, q := range []string{`"`, "'", "`", "“"} {
		closing := q
		if q == "“" {
			closing = "”"
		}
		if len(s) >= len(q)+len(closing) && strings.HasPrefix(s, q) && strings.HasSuffix(s, closing) {
			s = strings.TrimSpace(s[len(q) : len(s)-len(closing)])
		}
	}
	switch {
	case s == "", strings.EqualFold(strings.Trim(s, ".!"), "none"):
		return ""
	case len([]rune(s)) > suggestMaxChars:
		return ""
	case strings.HasPrefix(s, "/"), strings.HasPrefix(s, "!"):
		return "" // a command or shell line would run something unasked
	}
	return s
}
