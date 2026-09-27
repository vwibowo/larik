// Package agent runs the model ↔ tool loop. It knows nothing about the UI:
// front ends consume the Event stream returned by Run.
package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"larik/internal/checkpoint"
	"larik/internal/hooks"
	"larik/internal/llm"
	"larik/internal/lsp"
	"larik/internal/permission"
	"larik/internal/session"
	"larik/internal/skills"
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

	// LoadTools, if set, supplies the tool set at the start of each fresh
	// context (first prompt, and after Clear). The set then stays fixed so
	// the prompt prefix, and provider caches, remain stable. notify reports
	// progress or problems (e.g. an MCP server failing).
	LoadTools func(ctx context.Context, notify func(string)) *tools.Registry

	// Hooks runs user lifecycle hooks; nil disables them.
	Hooks *hooks.Runner

	// Skills expands "/skill-name args" prompts; nil disables that.
	Skills *skills.Set

	// LSP feeds language-server diagnostics back after edits; nil disables.
	LSP *lsp.Manager

	// Sandbox confines bash commands; nil runs them unconfined.
	Sandbox tools.Sandbox

	// Subagent is the agent type when this agent runs as a subagent; it
	// switches off session-level hooks and uses SubagentStop.
	Subagent string

	// NoAutoCompact turns off summarizing the conversation when the
	// context is nearly full; /compact still works.
	NoAutoCompact bool

	// Language, if set, is what the model replies in. It becomes part of
	// the system prompt.
	Language string

	// CompactWith, if set, picks the model that summarizes the
	// conversation; a nil provider means the agent's own model.
	CompactWith func() (llm.Provider, string)

	// Budget, if set, returns the session's spending cap in USD (0 for
	// none) and the fraction of it at which to warn. Subagents are held
	// to their parent's budget and total.
	Budget func() (capUSD, warnAt float64)

	// BuildSystem, if set, rebuilds System (without the language line) at
	// each fresh context, so edits to instruction files and new skills
	// apply after Clear. The prompt stays fixed within a context.
	BuildSystem func() string
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
	toolsLoaded bool

	sessionStarted bool
	startSource    string // SessionStart source: startup, resume, clear
	halted         bool   // a hook returned continue:false
	haltReason     string

	bg *background // lazily created; see background.go

	parent       *Agent // set for subagents; spend counts against its budget
	budgetWarned bool
	byModel      map[string]llm.Usage // spend per model, subagents included

	// baseSystem is opts.System without the language line. A language
	// change waits in nextLang for the next fresh context, so the prompt
	// prefix, and provider caches, stay stable meanwhile.
	baseSystem string
	nextLang   *string

	// Runtime facts from providers that can report them (see probe.go).
	windows map[string]int  // model -> context window actually in use
	probed  map[string]bool // model -> tool support already checked
	warned  map[string]bool // model -> truncation warning already shown
}

func New(opts Options) *Agent {
	if opts.MaxTurns == 0 {
		opts.MaxTurns = 200
	}
	a := &Agent{opts: opts, env: tools.NewEnv(opts.Cwd), startSource: "startup", baseSystem: opts.System}
	a.opts.System = WithLanguage(opts.System, opts.Language)
	if opts.Checkpoints != nil {
		a.env.BeforeWrite = func(path string) { _ = opts.Checkpoints.Capture(path) }
	}
	a.env.Sandbox = opts.Sandbox
	if opts.LSP.Enabled() {
		a.env.Diagnostics = opts.LSP.Diagnostics
		a.env.Touch = opts.LSP.Touch
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
	a.byModel = maps.Clone(st.ByModel)
	a.startSource = "resume"
}

func (a *Agent) Model() string              { return a.opts.Model }
func (a *Agent) Provider() llm.Provider     { return a.opts.Provider }
func (a *Agent) Cwd() string                { return a.opts.Cwd }
func (a *Agent) ProviderName() string       { return a.opts.Provider.Name() }
func (a *Agent) Perms() *permission.Checker { return a.opts.Perms }
func (a *Agent) SessionPath() string {
	if a.opts.Session == nil {
		return ""
	}
	return a.opts.Session.Path
}
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

// SetAutoCompact turns auto-compaction on or off.
func (a *Agent) SetAutoCompact(on bool) {
	a.mu.Lock()
	a.opts.NoAutoCompact = !on
	a.mu.Unlock()
}

// SetLanguage sets the reply language from the next fresh context on
// (after Clear, or in a new agent).
func (a *Agent) SetLanguage(lang string) {
	a.mu.Lock()
	a.nextLang = &lang
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
	return UsageInfo{Total: a.usage, CostUSD: a.cost, ContextTokens: a.lastContext, ContextWindow: a.windowLocked()}
}

// Clear drops the conversation context (the session file keeps history)
// and rebuilds the system prompt for the fresh context.
func (a *Agent) Clear() {
	var base string
	if a.opts.BuildSystem != nil {
		base = a.opts.BuildSystem() // reads files; done outside the lock
	}
	a.mu.Lock()
	a.messages, a.lastContext, a.toolsLoaded = nil, 0, false
	a.sessionStarted, a.startSource = false, "clear"
	if a.opts.BuildSystem != nil {
		a.baseSystem = base
	}
	if a.nextLang != nil {
		a.opts.Language, a.nextLang = *a.nextLang, nil
	}
	a.opts.System = WithLanguage(a.baseSystem, a.opts.Language)
	a.mu.Unlock()
}

// Tools returns the current tool registry.
func (a *Agent) Tools() *tools.Registry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.opts.Tools
}

func (a *Agent) loadTools(ctx context.Context, emit func(Event)) {
	a.mu.Lock()
	load := a.opts.LoadTools
	needed := !a.toolsLoaded && load != nil
	a.toolsLoaded = true
	a.mu.Unlock()
	if !needed {
		return
	}
	reg := load(ctx, func(msg string) { emit(Event{Kind: EvNotice, Text: msg}) })
	if reg != nil {
		a.mu.Lock()
		a.opts.Tools = reg
		a.mu.Unlock()
	}
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
		reason := a.runWith(ctx, prompt, false, func(e Event) { ch <- e })
		ch <- Event{Kind: EvDone, StopReason: reason}
	}()
	return ch
}

// runWith runs one turn. system marks Larik-generated prompts (background
// notifications), which skip prompt hooks and skill expansion.
func (a *Agent) runWith(ctx context.Context, prompt string, system bool, emit func(Event)) string {
	a.loadTools(ctx, emit)
	sub := a.opts.Subagent != ""
	quiet := sub || system // no session/prompt hooks or skill expansion
	if !quiet {
		a.sessionStart(ctx, emit)
	}
	a.takeHalt() // clear any stale request

	var res hooks.Result
	if !quiet {
		res = a.runHook(ctx, emit, hooks.Input{HookEventName: hooks.UserPromptSubmit, Prompt: prompt}, "")
	}
	switch {
	case res.Halt:
		emit(Event{Kind: EvNotice, Text: "stopped by UserPromptSubmit hook: " + res.HaltReason})
		return "hook_stopped"
	case res.Block:
		emit(Event{Kind: EvNotice, Text: "prompt blocked by hook: " + res.Reason})
		return "blocked"
	}
	if expanded, ok := a.opts.Skills.Expand(prompt); ok && !quiet {
		name, _, _ := strings.Cut(strings.TrimPrefix(prompt, "/"), " ")
		emit(Event{Kind: EvNotice, Text: "running skill /" + name})
		prompt = expanded
	}
	if len(res.Context) > 0 {
		prompt += "\n\n" + hookContext("UserPromptSubmit", res.Context)
	}

	if a.opts.Checkpoints != nil && !sub { // subagent edits belong to the parent's turn
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
	stopContinuations := 0
	var loops loopGuard
	for turn := 0; turn < a.opts.MaxTurns; turn++ {
		if turn > 0 && a.needsCompaction() && !compactedThisTurn {
			if err := a.compact(ctx, emit, true); err != nil {
				emit(Event{Kind: EvNotice, Text: "auto-compaction failed: " + err.Error()})
			}
			compactedThisTurn = true
		}

		// Background results that finished mid-turn ride along with the
		// next request, appended after the latest user content.
		if note := a.takeNotifications(); note != "" {
			a.appendMessage(llm.UserText(note), nil)
		}

		if err := a.checkBudget(emit); err != nil {
			emit(Event{Kind: EvError, Text: err.Error()})
			return "budget"
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
			stopEvent, source := hooks.Stop, "Stop"
			if sub {
				stopEvent, source = hooks.SubagentStop, "SubagentStop"
			}
			res := a.runHook(ctx, emit, hooks.Input{HookEventName: stopEvent, StopHookActive: stopContinuations > 0, AgentType: a.opts.Subagent}, a.opts.Subagent)
			if res.Halt {
				emit(Event{Kind: EvNotice, Text: "stopped by " + source + " hook: " + res.HaltReason})
				return "hook_stopped"
			}
			if res.Block && res.Reason != "" && ctx.Err() == nil {
				if stopContinuations >= maxStopContinuations {
					emit(Event{Kind: EvNotice, Text: source + " hook asked to continue again; ignoring after 5 continuations"})
					return string(stop)
				}
				stopContinuations++
				emit(Event{Kind: EvNotice, Text: source + " hook asked the agent to continue: " + firstLine(res.Reason)})
				a.appendMessage(llm.UserText(hookFeedback(source, res.Reason)), nil)
				continue
			}
			return string(stop)
		}

		results := a.runTools(ctx, uses, emit)
		a.appendMessage(llm.Message{Role: llm.RoleUser, Blocks: results}, nil)
		if ctx.Err() != nil {
			return "interrupted"
		}
		if sub && loops.see(uses, results) {
			emit(Event{Kind: EvError, Text: fmt.Sprintf("stopped: the subagent repeated the same %s call with the same result %d times; it looks stuck", uses[0].Name, loopRepeats)})
			return "loop"
		}
		if halted, reason := a.takeHalt(); halted {
			emit(Event{Kind: EvNotice, Text: "stopped by hook: " + reason})
			return "hook_stopped"
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
	a.checkTools(ctx, provider, req.Model, len(req.Tools) > 0, emit)

	var partial strings.Builder
	// How long the model thought: from sending the request until its
	// first text or tool call. Measured this way because some providers
	// send reasoning only at the end, just before the answer.
	start := time.Now()
	var thought time.Duration
	endThinking := func() {
		if thought == 0 {
			thought = time.Since(start)
		}
	}
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
			endThinking()
			partial.WriteString(ev.Text)
			emit(Event{Kind: EvTextDelta, Text: ev.Text})
		case llm.EventThinkingDelta:
			emit(Event{Kind: EvThinkingDelta, Text: ev.Text})
		case llm.EventToolUseStart:
			endThinking()
			emit(Event{Kind: EvToolCallDelta, ToolName: ev.Text})
		case llm.EventNotice:
			emit(Event{Kind: EvNotice, Text: ev.Text})
		case llm.EventDone:
			endThinking()
			msg := ev.Message
			if thought > 0 {
				for i := range msg.Blocks {
					if msg.Blocks[i].Type == llm.BlockThinking {
						msg.Blocks[i].DurationMS = thought.Milliseconds()
						break
					}
				}
			}
			if len(msg.Blocks) == 0 {
				msg.Blocks = []llm.Block{llm.TextBlock("(empty response)")}
			}
			if msg.Model == "" {
				msg.Model = req.Model
			}
			a.probeWindow(ctx, provider, msg.Model, ev.Usage, emit)
			a.recordUsage(msg.Model, ev.Usage, emit)
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

// recordUsage adds a request's usage, priced for the model that served it.
func (a *Agent) recordUsage(model string, u llm.Usage, emit func(Event)) {
	a.mu.Lock()
	info := llm.Lookup(model)
	a.usage.Add(u)
	a.cost += info.Cost(u)
	a.addModelUsageLocked(model, u)
	a.lastContext = u.ContextTokens() + u.Output
	ui := UsageInfo{Model: model, Turn: u, Total: a.usage, CostUSD: a.cost, ContextTokens: a.lastContext, ContextWindow: a.windowLocked()}
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
	// With only a prompt and an answer there is nothing worth summarizing;
	// compacting would just throw the conversation away (tiny local windows
	// can be exceeded by the system prompt alone).
	if len(a.messages) < 4 || a.opts.NoAutoCompact {
		return false
	}
	return a.lastContext > 0 && float64(a.lastContext) > compactThreshold*float64(a.windowLocked())
}

// Compact summarizes the conversation into a single message ("simple
// compaction": nothing from the old transcript is replayed afterwards).
func (a *Agent) Compact(ctx context.Context) (string, error) {
	var summary string
	err := a.compactWith(ctx, func(e Event) {
		if e.Kind == EvCompacted {
			summary = e.Summary
		}
	}, false, "manual")
	return summary, err
}

func (a *Agent) compact(ctx context.Context, emit func(Event), midTurn bool) error {
	return a.compactWith(ctx, emit, midTurn, "auto")
}

func (a *Agent) compactWith(ctx context.Context, emit func(Event), midTurn bool, trigger string) error {
	a.runHook(ctx, emit, hooks.Input{HookEventName: hooks.PreCompact, Trigger: trigger}, trigger)
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
	pick := a.opts.CompactWith
	a.mu.Unlock()
	if len(msgs) == 0 {
		return errors.New("nothing to compact")
	}
	if pick != nil {
		if p, m := pick(); p != nil && m != "" {
			provider, req.Model = p, m
			req.MaxTokens = min(req.MaxTokens, llm.Lookup(m).MaxOutput)
		}
	}
	req.Messages = withUserText(msgs, compactionPrompt)

	var final llm.Message
	for ev, err := range provider.Stream(ctx, req) {
		if err != nil {
			return err
		}
		switch ev.Type {
		case llm.EventNotice:
			emit(Event{Kind: EvNotice, Text: ev.Text})
		case llm.EventDone:
			final = ev.Message
			model := final.Model
			if model == "" {
				model = req.Model
			}
			// The summary replaces the transcript's context rather than
			// joining it, so its spend is recorded on its own.
			a.recordUsage(model, ev.Usage, emit)
			if a.opts.Session != nil {
				_ = a.opts.Session.AppendUsage(model, ev.Usage)
			}
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
	a.messages = []llm.Message{session.CompactionMessage(summary, a.SessionPath())}
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

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// SetTools replaces the tool registry (tests and embedders; normally tools
// come from Options or LoadTools).
func (a *Agent) SetTools(r *tools.Registry) {
	a.mu.Lock()
	a.opts.Tools, a.toolsLoaded = r, true
	a.mu.Unlock()
}
