// Package agent runs the model ↔ tool loop. It knows nothing about the UI:
// front ends consume the Event stream returned by Run.
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"larik/internal/checkpoint"
	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/session"
	"larik/internal/tools"
)

// compactThreshold is the fraction of the context window that triggers
// automatic compaction before the next request.
const compactThreshold = 0.8

type Options struct {
	Provider    llm.Provider
	Model       string
	Effort      llm.Effort
	System      string
	Cwd         string
	MaxTurns    int
	Tools       *tools.Registry
	Perms       *permission.Checker
	Session     *session.Session  // optional
	Checkpoints *checkpoint.Store // optional
	OnAllowRule func(rule string) // persists "always allow" answers
}

type Agent struct {
	opts Options
	env  *tools.Env

	mu          sync.Mutex
	messages    []llm.Message
	usage       llm.Usage
	cost        float64
	lastContext int
	notes       []string // prepended to the next user message
}

func New(opts Options) *Agent {
	if opts.MaxTurns == 0 {
		opts.MaxTurns = 200
	}
	a := &Agent{opts: opts, env: tools.NewEnv(opts.Cwd)}
	if opts.Checkpoints != nil {
		a.env.BeforeWrite = func(path string) { _ = opts.Checkpoints.Capture(path) }
	}
	return a
}

// Restore loads a resumed session's state.
func (a *Agent) Restore(st *session.State) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = st.Messages
	a.usage = st.Usage
	a.cost = st.Cost
}

func (a *Agent) Model() string              { return a.opts.Model }
func (a *Agent) ProviderName() string       { return a.opts.Provider.Name() }
func (a *Agent) Perms() *permission.Checker { return a.opts.Perms }
func (a *Agent) SessionID() string {
	if a.opts.Session == nil {
		return ""
	}
	return a.opts.Session.ID
}

// SetModel switches provider/model for subsequent turns. Provider-bound
// blocks (thinking, reasoning items) are filtered out automatically.
func (a *Agent) SetModel(p llm.Provider, model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.opts.Provider, a.opts.Model = p, model
}

func (a *Agent) SetEffort(e llm.Effort) {
	a.mu.Lock()
	a.opts.Effort = e
	a.mu.Unlock()
}

func (a *Agent) Effort() llm.Effort {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.opts.Effort
}

// Stats reports session totals for the status bar.
func (a *Agent) Stats() UsageInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	return UsageInfo{Total: a.usage, CostUSD: a.cost, ContextTokens: a.lastContext, ContextWindow: llm.Lookup(a.opts.Model).ContextWindow}
}

// Clear drops the conversation context (the session file keeps history).
func (a *Agent) Clear() {
	a.mu.Lock()
	a.messages, a.lastContext = nil, 0
	a.mu.Unlock()
}

// Undo reverts the last turn's file changes and tells the model about it
// on the next message, keeping the transcript append-only.
func (a *Agent) Undo() ([]string, error) {
	if a.opts.Checkpoints == nil {
		return nil, errors.New("checkpoints are disabled")
	}
	paths, err := a.opts.Checkpoints.Undo()
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.notes = append(a.notes, "The user reverted your most recent file changes to: "+strings.Join(paths, ", ")+". Re-read these files before editing them.")
	a.mu.Unlock()
	return paths, nil
}

// Run processes one user prompt. The returned channel is closed after EvDone.
// Callers must drain it and answer every EvPermission.
func (a *Agent) Run(ctx context.Context, prompt string) <-chan Event {
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		reason := a.run(ctx, prompt, func(e Event) { ch <- e })
		ch <- Event{Kind: EvDone, StopReason: reason}
	}()
	return ch
}

func (a *Agent) run(ctx context.Context, prompt string, emit func(Event)) string {
	if a.opts.Checkpoints != nil {
		a.opts.Checkpoints.BeginTurn()
	}
	if a.needsCompaction() {
		if err := a.compact(ctx, emit, false); err != nil {
			emit(Event{Kind: EvNotice, Text: "auto-compaction failed: " + err.Error()})
		}
	}

	a.mu.Lock()
	if len(a.notes) > 0 {
		prompt = "<system-note>\n" + strings.Join(a.notes, "\n") + "\n</system-note>\n\n" + prompt
		a.notes = nil
	}
	a.mu.Unlock()
	a.appendMessage(llm.UserText(prompt), nil)

	compactedThisTurn := false
	for turn := 0; turn < a.opts.MaxTurns; turn++ {
		if turn > 0 && a.needsCompaction() && !compactedThisTurn {
			if err := a.compact(ctx, emit, true); err != nil {
				emit(Event{Kind: EvNotice, Text: "auto-compaction failed: " + err.Error()})
			}
			compactedThisTurn = true
		}

		msg, stop, err := a.stream(ctx, emit)
		if errors.Is(err, llm.ErrContextOverflow) && !compactedThisTurn {
			emit(Event{Kind: EvNotice, Text: "context window full; compacting"})
			compactedThisTurn = true
			if cerr := a.compact(ctx, emit, true); cerr != nil {
				emit(Event{Kind: EvError, Text: cerr.Error()})
				return "error"
			}
			msg, stop, err = a.stream(ctx, emit)
		}
		if ctx.Err() != nil {
			return "interrupted"
		}
		if err != nil {
			emit(Event{Kind: EvError, Text: err.Error()})
			return "error"
		}

		uses := msg.ToolUses()
		if len(uses) == 0 {
			switch stop {
			case llm.StopMaxTokens:
				emit(Event{Kind: EvNotice, Text: "response hit the output token limit"})
			case llm.StopRefusal:
				emit(Event{Kind: EvNotice, Text: "the model declined to continue this request"})
			}
			return string(stop)
		}

		results := a.runTools(ctx, uses, emit)
		a.appendMessage(llm.Message{Role: llm.RoleUser, Blocks: results}, nil)
		if ctx.Err() != nil {
			return "interrupted"
		}
	}
	emit(Event{Kind: EvNotice, Text: fmt.Sprintf("stopped after %d model turns (max_turns)", a.opts.MaxTurns)})
	return "max_turns"
}

// stream makes one model request and records the assistant message.
func (a *Agent) stream(ctx context.Context, emit func(Event)) (llm.Message, llm.StopReason, error) {
	a.mu.Lock()
	req := llm.Request{
		Model:     a.opts.Model,
		System:    a.opts.System,
		Messages:  append([]llm.Message(nil), a.messages...),
		Tools:     a.opts.Tools.Specs(),
		MaxTokens: min(llm.Lookup(a.opts.Model).MaxOutput, 64_000),
		Effort:    a.opts.Effort,
	}
	provider := a.opts.Provider
	a.mu.Unlock()

	var partial strings.Builder
	for ev, err := range provider.Stream(ctx, req) {
		if err != nil {
			if ctx.Err() != nil {
				a.keepPartial(partial.String())
				return llm.Message{}, "", ctx.Err()
			}
			return llm.Message{}, "", err
		}
		switch ev.Type {
		case llm.EventTextDelta:
			partial.WriteString(ev.Text)
			emit(Event{Kind: EvTextDelta, Text: ev.Text})
		case llm.EventThinkingDelta:
			emit(Event{Kind: EvThinkingDelta, Text: ev.Text})
		case llm.EventToolUseStart:
			emit(Event{Kind: EvToolCallDelta, ToolName: ev.Text})
		case llm.EventDone:
			msg := ev.Message
			if len(msg.Blocks) == 0 {
				msg.Blocks = []llm.Block{llm.TextBlock("(empty response)")}
			}
			a.recordUsage(ev.Usage, emit)
			a.appendMessage(msg, &ev.Usage)
			emit(Event{Kind: EvAssistant, Message: &msg})
			return msg, ev.StopReason, nil
		}
	}
	if ctx.Err() != nil {
		a.keepPartial(partial.String())
		return llm.Message{}, "", ctx.Err()
	}
	return llm.Message{}, "", errors.New("stream ended without a final message")
}

// keepPartial records text the user already saw before an interrupt so the
// transcript matches what was displayed.
func (a *Agent) keepPartial(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	a.appendMessage(llm.Message{Role: llm.RoleAssistant, Model: a.opts.Model, Blocks: []llm.Block{llm.TextBlock(text + "\n\n[interrupted by user]")}}, nil)
}

func (a *Agent) recordUsage(u llm.Usage, emit func(Event)) {
	a.mu.Lock()
	info := llm.Lookup(a.opts.Model)
	a.usage.Add(u)
	a.cost += info.Cost(u)
	a.lastContext = u.ContextTokens() + u.Output
	ui := UsageInfo{Turn: u, Total: a.usage, CostUSD: a.cost, ContextTokens: a.lastContext, ContextWindow: info.ContextWindow}
	a.mu.Unlock()
	emit(Event{Kind: EvUsage, Usage: &ui})
}

func (a *Agent) appendMessage(m llm.Message, usage *llm.Usage) {
	a.mu.Lock()
	a.messages = llm.Append(a.messages, m)
	a.mu.Unlock()
	if a.opts.Session != nil {
		_ = a.opts.Session.AppendMessage(m, usage)
	}
}

func (a *Agent) needsCompaction() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	window := llm.Lookup(a.opts.Model).ContextWindow
	return a.lastContext > 0 && float64(a.lastContext) > compactThreshold*float64(window)
}

// Compact summarizes the conversation into a single message ("simple
// compaction": nothing from the old transcript is replayed afterwards).
func (a *Agent) Compact(ctx context.Context) (string, error) {
	var summary string
	err := a.compactWith(ctx, func(e Event) {
		if e.Kind == EvCompacted {
			summary = e.Summary
		}
	}, false)
	return summary, err
}

func (a *Agent) compact(ctx context.Context, emit func(Event), midTurn bool) error {
	return a.compactWith(ctx, emit, midTurn)
}

func (a *Agent) compactWith(ctx context.Context, emit func(Event), midTurn bool) error {
	a.mu.Lock()
	msgs := append([]llm.Message(nil), a.messages...)
	req := llm.Request{
		Model:     a.opts.Model,
		System:    a.opts.System,
		Tools:     a.opts.Tools.Specs(), // unchanged tools keep the cached prefix valid
		MaxTokens: 32_000,
		Effort:    a.opts.Effort,
	}
	provider := a.opts.Provider
	a.mu.Unlock()
	if len(msgs) == 0 {
		return errors.New("nothing to compact")
	}
	req.Messages = withUserText(msgs, compactionPrompt)

	var final llm.Message
	for ev, err := range provider.Stream(ctx, req) {
		if err != nil {
			return err
		}
		if ev.Type == llm.EventDone {
			final = ev.Message
			a.recordUsage(ev.Usage, emit)
		}
	}
	summary := extractSummary(final.Text())
	if summary == "" {
		return errors.New("model returned an empty summary")
	}
	if midTurn {
		summary += "\n\nContinue the task from where it left off."
	}

	a.mu.Lock()
	a.messages = []llm.Message{session.CompactionMessage(summary)}
	a.lastContext = 0
	a.mu.Unlock()
	if a.opts.Session != nil {
		_ = a.opts.Session.AppendCompaction(summary)
	}
	emit(Event{Kind: EvCompacted, Summary: summary})
	return nil
}

// withUserText appends text as a user turn, merging into a trailing user
// message so roles keep alternating.
func withUserText(msgs []llm.Message, text string) []llm.Message {
	return llm.Append(msgs, llm.UserText(text))
}

func extractSummary(s string) string {
	if i := strings.Index(s, "<summary>"); i >= 0 {
		s = s[i+len("<summary>"):]
		if j := strings.Index(s, "</summary>"); j >= 0 {
			s = s[:j]
		}
	}
	return strings.TrimSpace(s)
}
