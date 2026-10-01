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
	"sync/atomic"
	"time"

	"larik/internal/checkpoint"
	"larik/internal/hooks"
	"larik/internal/llm"
	"larik/internal/lsp"
	"larik/internal/permission"
	"larik/internal/session"
	"larik/internal/skills"
	"larik/internal/tools"
	"larik/internal/trace"
)

// compactThreshold is the fraction of the context window that triggers
// automatic compaction before the next request.
const compactThreshold = 0.8

type Options struct {
	Provider    llm.Provider
	Runtime     llm.AgentRuntime
	Model       string
	Effort      llm.Effort
	System      string
	Cwd         string
	MaxTurns    int
	Tools       *tools.Registry
	Perms       *permission.Checker
	Session     *session.Session        // optional
	Checkpoints *checkpoint.Store       // optional
	OnAllowRule func(rule string) error // persists "always allow" answers
	// AutoApprove judges calls in auto mode; nil sends them all to the user.
	AutoApprove AutoApprover

	// Execution is how the model carries out actions: with tools only,
	// or also (or only) with run_code scripts. Empty means tools.
	Execution tools.Execution
	// ExecutionFor, if set, gives the execution for a provider and model:
	// it is applied when the model changes (SetModel) and to subagents,
	// which may run another model.
	ExecutionFor func(provider, model string) tools.Execution

	// LoadTools, if set, supplies the tool set at the start of each fresh
	// context (first prompt, and after Clear). The set then stays fixed so
	// the prompt prefix, and provider caches, remain stable. notify reports
	// progress or problems (e.g. an MCP server failing).
	LoadTools func(ctx context.Context, notify func(string)) *tools.Registry

	// Hooks runs user lifecycle hooks; nil disables them.
	Hooks *hooks.Runner

	// Skills expands "/skill-name args" prompts; nil disables that.
	Skills *skills.Set
	// MCP, if set, reads MCP resources for @server:uri mentions and runs
	// /mcp__server__prompt commands.
	MCP MCPContent

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
	TokenSaver    bool

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

	// Trace records requests, responses and tool calls in debug mode;
	// nil records nothing. SetTrace changes it.
	Trace *trace.Tracer
}

type Agent struct {
	opts       Options
	env        *tools.Env
	tokenSaver *atomic.Bool

	mu          sync.Mutex
	messages    []llm.Message
	usage       llm.Usage
	cost        float64
	lastContext int
	notes       []string // prepended to the next user message
	planNoted   bool     // the model was last told plan mode is on
	// saveErr is the first failure to write the session transcript, and
	// saveReported whether the user has been told about it.
	saveErr      error
	saveReported bool
	pending      []llm.Block // attached to the next user message, e.g. "!" output
	toolsLoaded  bool
	// active is opts.Tools as the model sees it under the execution
	// setting; activeFor records what it was built from.
	active    *tools.Registry
	activeFor struct {
		base *tools.Registry
		exec tools.Execution
	}

	sessionStarted bool
	startSource    string // SessionStart source: startup, resume, clear
	halted         bool   // a hook returned continue:false
	haltReason     string

	bg *background // lazily created; see background.go

	parent *Agent // set for subagents; spend counts against its budget
	// owner identifies a subagent to tools that keep per-agent state (the
	// browser's tabs); "" for the main agent.
	owner string
	// autoAllowed remembers the calls auto mode approved (tool and input),
	// kept on the root agent for its subagents too.
	autoAllowed  map[string]bool
	budgetWarned bool
	byModel      map[string]llm.Usage // spend per model, subagents included

	// baseSystem is opts.System without the language line. A language
	// change waits in nextLang for the next fresh context, so the prompt
	// prefix, and provider caches, stay stable meanwhile.
	baseSystem string
	nextLang   *string
	// nextExecution waits for a fresh context so the declared tool list
	// stays stable throughout the current conversation.
	nextExecution *tools.Execution

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
	a.tokenSaver = &atomic.Bool{}
	a.tokenSaver.Store(opts.TokenSaver)
	a.env.TokenSaver = a.tokenSaver
	if opts.Session != nil {
		a.env.RawOutputDir = session.RawDir(opts.Session.Path)
	}
	a.opts.System = WithLanguage(opts.System, opts.Language)
	if opts.Checkpoints != nil {
		a.env.BeforeWrite = opts.Checkpoints.Capture
		a.env.RecordOriginal = opts.Checkpoints.Record
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

// Model and Provider can change (SetModel) while other goroutines read
// them, e.g. the server answering a status request mid-turn.
func (a *Agent) Model() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.opts.Model
}

func (a *Agent) Provider() llm.Provider {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.opts.Provider
}

func (a *Agent) ProviderName() string { return a.Provider().Name() }

func (a *Agent) Cwd() string                { return a.opts.Cwd }
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

// SetTokenSaver applies a settings change to subsequent tool calls, including children.
func (a *Agent) SetTokenSaver(on bool) { a.tokenSaver.Store(on) }

func (a *Agent) TokenSaverOn() bool { return a.tokenSaver.Load() }

// SetModel switches provider/model for subsequent turns. Provider-bound
// blocks (thinking, reasoning items) are filtered out automatically.
func (a *Agent) SetModel(p llm.Provider, model string, runtime ...llm.AgentRuntime) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.opts.Provider, a.opts.Model = p, model
	if a.opts.ExecutionFor != nil && p != nil {
		a.opts.Execution = a.opts.ExecutionFor(p.Name(), model)
		a.nextExecution = nil
	}
	a.opts.Runtime = nil
	if len(runtime) > 0 {
		a.opts.Runtime = runtime[0]
	}
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
	if a.nextExecution != nil {
		a.opts.Execution, a.nextExecution = *a.nextExecution, nil
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

// activeTools returns the registry the model sees and calls resolve in.
func (a *Agent) activeTools() *tools.Registry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.activeLocked()
}

func (a *Agent) activeLocked() *tools.Registry {
	base, exec := a.opts.Tools, a.opts.Execution
	if base == nil {
		return nil
	}
	if a.active == nil || a.activeFor.base != base || a.activeFor.exec != exec {
		a.active = base.ForExecution(exec)
		a.activeFor.base, a.activeFor.exec = base, exec
	}
	return a.active
}

// Execution reports how the model carries out actions.
func (a *Agent) Execution() tools.Execution {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.opts.Execution == "" {
		return tools.ExecTools
	}
	return a.opts.Execution
}

// SetExecution changes how the model carries out actions. Once a context
// has messages, the change waits for Clear so its tool list stays stable.
func (a *Agent) SetExecution(e tools.Execution) {
	a.mu.Lock()
	if len(a.messages) == 0 {
		a.opts.Execution = e
		a.nextExecution = nil
	} else if e == a.opts.Execution {
		a.nextExecution = nil
	} else {
		a.nextExecution = &e
	}
	a.mu.Unlock()
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
		send := func(e Event) { ch <- e }
		reason := a.runWith(ctx, prompt, false, send)
		a.reportSaveError(send) // a write at the very end of the turn
		ch <- Event{Kind: EvDone, StopReason: reason}
	}()
	return ch
}

// runWith runs one turn. system marks Larik-generated prompts (background
// notifications), which skip prompt hooks and skill expansion.
func (a *Agent) runWith(ctx context.Context, prompt string, system bool, emit func(Event)) string {
	emit = a.traceEvents(a.reportingSaveErrors(emit))
	stop := a.runTurn(ctx, prompt, system, emit)
	a.tracer().TurnEnd(stop)
	return stop
}

func (a *Agent) runTurn(ctx context.Context, prompt string, system bool, emit func(Event)) string {
	typed := prompt
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
	var attached []llm.Block
	if !quiet { // mentions in the prompt as typed, not in a skill's body
		attached = a.resolveMentions(ctx, prompt, emit)
	}
	if rest, ok := strings.CutPrefix(prompt, InitCommand); ok && !quiet && (rest == "" || rest[0] == ' ') {
		emit(Event{Kind: EvNotice, Text: "running /init"})
		prompt = a.initPrompt(rest)
	} else if a.opts.MCP != nil && !quiet && strings.HasPrefix(prompt, "/mcp__") {
		text, ok, err := a.opts.MCP.ExpandPrompt(ctx, prompt)
		switch {
		case err != nil:
			emit(Event{Kind: EvError, Text: err.Error()})
			return "error"
		case ok:
			name, _, _ := strings.Cut(strings.TrimPrefix(prompt, "/"), " ")
			emit(Event{Kind: EvNotice, Text: "running /" + name})
			prompt = text
		}
	} else if expanded, ok := a.opts.Skills.Expand(prompt); ok && !quiet {
		name, _, _ := strings.Cut(strings.TrimPrefix(prompt, "/"), " ")
		emit(Event{Kind: EvNotice, Text: "running /" + name})
		if sk, _ := a.opts.Skills.Get(name); sk.Builtin {
			// A built-in command has no inline commands; its changes
			// marker is filled here, and the diff must never go through
			// runInline, which would run any !`command` quoted in it.
			_, args, _ := strings.Cut(prompt, " ")
			prompt = strings.Replace(expanded, skills.ChangesMarker, a.reviewChanges(ctx, strings.TrimSpace(args), emit), 1)
		} else {
			prompt = a.runInline(ctx, name, expanded, emit)
		}
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
	a.planNotesLocked()
	if len(a.notes) > 0 {
		prompt = "<system-note>\n" + strings.Join(a.notes, "\n") + "\n</system-note>\n\n" + prompt
		a.notes = nil
	}
	user := llm.UserText(prompt)
	if !quiet {
		user.Blocks = append(append(user.Blocks, a.pending...), attached...)
		a.pending = nil
	}
	a.mu.Unlock()
	a.answerDangling()
	a.tracer().TurnStart(typed, user.Text(), attachmentNames(attached))
	a.appendMessage(user, nil)
	if a.opts.Runtime != nil {
		return a.runRuntime(ctx, emit)
	}

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
		if err != nil {
			if ctx.Err() != nil {
				return "interrupted"
			}
			emit(Event{Kind: EvError, Text: err.Error()})
			return "error"
		}

		// The reply is saved by now. If it called tools, answer every call
		// even when interrupted (runTools reports each as interrupted), so
		// the transcript never ends in an unanswered tool_use.
		uses := msg.ToolUses()
		if len(uses) == 0 {
			if ctx.Err() != nil {
				return "interrupted"
			}
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

// runRuntime delegates the full model/tool loop to a process-backed runtime.
// Tool requests re-enter runTools so permission checks, hooks, and transcript
// results follow the same path as API-backed providers.
func (a *Agent) runRuntime(ctx context.Context, emit func(Event)) string {
	stopContinuations := 0
	for {
		reason := a.runRuntimePass(ctx, emit)
		if reason != "stop" {
			return reason
		}
		stopEvent, source := hooks.Stop, "Stop"
		if a.opts.Subagent != "" {
			stopEvent, source = hooks.SubagentStop, "SubagentStop"
		}
		res := a.runHook(ctx, emit, hooks.Input{HookEventName: stopEvent, StopHookActive: stopContinuations > 0, AgentType: a.opts.Subagent}, a.opts.Subagent)
		if res.Halt {
			emit(Event{Kind: EvNotice, Text: "stopped by " + source + " hook: " + res.HaltReason})
			return "hook_stopped"
		}
		if !res.Block || res.Reason == "" || ctx.Err() != nil {
			return "end_turn"
		}
		if stopContinuations >= maxStopContinuations {
			emit(Event{Kind: EvNotice, Text: source + " hook asked to continue again; ignoring after 5 continuations"})
			return "end_turn"
		}
		stopContinuations++
		emit(Event{Kind: EvNotice, Text: source + " hook asked the agent to continue: " + firstLine(res.Reason)})
		a.appendMessage(llm.UserText(hookFeedback(source, res.Reason)), nil)
	}
}

func (a *Agent) runRuntimePass(ctx context.Context, emit func(Event)) string {
	var loops loopGuard
	a.mu.Lock()
	req := llm.AgentRuntimeRequest{Model: a.opts.Model, System: a.opts.System, Workspace: a.opts.Cwd,
		Messages: append([]llm.Message(nil), a.messages...), Tools: a.activeLocked().Specs(), Effort: a.opts.Effort, MaxTurns: a.opts.MaxTurns}
	runtime := a.opts.Runtime
	a.mu.Unlock()
	runtimeCtx, cancelRuntime := context.WithCancel(ctx)
	defer cancelRuntime()
	events, err := runtime.Run(runtimeCtx, req)
	if err != nil {
		emit(Event{Kind: EvError, Text: err.Error()})
		return "error"
	}
	for event := range events {
		if event.Text != "" {
			emit(Event{Kind: EvTextDelta, Text: event.Text})
		}
		if event.Thinking != "" {
			emit(Event{Kind: EvThinkingDelta, Text: event.Thinking})
		}
		if event.Assistant != nil {
			msg := *event.Assistant
			if msg.Model == "" {
				msg.Model = req.Model
			}
			a.appendMessage(msg, nil)
			emit(Event{Kind: EvAssistant, Message: &msg})
		}
		if event.Tool != nil {
			call := event.Tool.Call
			uses := []llm.Block{call}
			results := a.runTools(ctx, uses, emit)
			if len(results) != 0 {
				a.appendMessage(llm.Message{Role: llm.RoleUser, Blocks: results}, nil)
				select {
				case event.Tool.Result <- results[0]:
				case <-ctx.Done():
					return "interrupted"
				}
			}
			if a.opts.Subagent != "" && loops.see(uses, results) {
				cancelRuntime()
				emit(Event{Kind: EvError, Text: fmt.Sprintf("stopped: the subagent repeated the same %s call with the same result %d times; it looks stuck", call.Name, loopRepeats)})
				return "loop"
			}
			if halted, reason := a.takeHalt(); halted {
				cancelRuntime()
				emit(Event{Kind: EvNotice, Text: "stopped by hook: " + reason})
				return "hook_stopped"
			}
		}
		if event.Usage != nil {
			a.recordUsage(req.Model, *event.Usage, emit)
		}
		if event.Err != nil {
			if ctx.Err() != nil {
				return "interrupted"
			}
			emit(Event{Kind: EvError, Text: event.Err.Error()})
			return "error"
		}
		if event.Done {
			return "stop"
		}
	}
	if ctx.Err() != nil {
		return "interrupted"
	}
	emit(Event{Kind: EvError, Text: "Claude Code CLI runtime ended without completing the turn"})
	return "error"
}

// stream makes one model request and records the assistant message.
func (a *Agent) stream(ctx context.Context, emit func(Event)) (llm.Message, llm.StopReason, error) {
	a.mu.Lock()
	req := llm.Request{
		Model:     a.opts.Model,
		System:    a.opts.System,
		Messages:  append([]llm.Message(nil), a.messages...),
		Tools:     a.activeLocked().Specs(),
		MaxTokens: min(llm.Lookup(a.opts.Model).MaxOutput, 64_000),
		Effort:    a.opts.Effort,
	}
	provider := a.opts.Provider
	tr := a.opts.Trace
	a.mu.Unlock()
	a.checkTools(ctx, provider, req.Model, len(req.Tools) > 0, emit)
	reqID := tr.Request(provider.Name(), req, "")
	ctx = tr.Wire(ctx, reqID)

	var partial strings.Builder
	// How long the model thought: from sending the request until its
	// first text or tool call. Measured this way because some providers
	// send reasoning only at the end, just before the answer.
	start := time.Now()
	var thought, ttft time.Duration
	endThinking := func() {
		if thought == 0 {
			thought = time.Since(start)
		}
	}
	var notices []string
	traced := func(r trace.Response) {
		r.Start, r.DurationMS = start.UnixMilli(), time.Since(start).Milliseconds()
		r.TTFTMS, r.ThinkingMS, r.Notices = ttft.Milliseconds(), thought.Milliseconds(), notices
		tr.Response(reqID, r)
	}
	for ev, err := range provider.Stream(ctx, req) {
		if err != nil {
			traced(trace.Response{Error: err.Error()})
			if ctx.Err() != nil {
				a.keepPartial(partial.String())
				return llm.Message{}, "", ctx.Err()
			}
			return llm.Message{}, "", err
		}
		if ttft == 0 && ev.Type != llm.EventNotice {
			ttft = time.Since(start)
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
			notices = append(notices, ev.Text)
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
			usage := ev.Usage
			traced(trace.Response{Message: &msg, Usage: &usage, CostUSD: llm.Lookup(msg.Model).Cost(ev.Usage), StopReason: string(ev.StopReason)})
			a.probeWindow(ctx, provider, msg.Model, ev.Usage, emit)
			a.recordUsage(msg.Model, ev.Usage, emit)
			a.appendMessage(msg, &ev.Usage)
			emit(Event{Kind: EvAssistant, Message: &msg})
			return msg, ev.StopReason, nil
		}
	}
	if ctx.Err() != nil {
		traced(trace.Response{Error: ctx.Err().Error()})
		a.keepPartial(partial.String())
		return llm.Message{}, "", ctx.Err()
	}
	traced(trace.Response{Error: "stream ended without a final message"})
	return llm.Message{}, "", errors.New("stream ended without a final message")
}

// answerDangling adds "interrupted" results for tool calls the context
// left unanswered (a session saved mid-turn, or by an older version), so
// the next request is valid. It appends; the transcript isn't rewritten.
func (a *Agent) answerDangling() {
	var missing []llm.Block
	a.mu.Lock()
	if n := len(a.messages); n > 0 && a.messages[n-1].Role == llm.RoleAssistant {
		for _, u := range a.messages[n-1].ToolUses() {
			missing = append(missing, llm.Block{Type: llm.BlockToolResult, ID: u.ID, Name: u.Name, Content: "interrupted by user", IsError: true})
		}
	}
	a.mu.Unlock()
	if len(missing) > 0 {
		a.appendMessage(llm.Message{Role: llm.RoleUser, Blocks: missing}, nil)
	}
}

// keepPartial records text the user already saw before an interrupt so the
// transcript matches what was displayed.
func (a *Agent) keepPartial(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	a.appendMessage(llm.Message{Role: llm.RoleAssistant, Model: a.Model(), Blocks: []llm.Block{llm.TextBlock(text + "\n\n[interrupted by user]")}}, nil)
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
		a.saveFailed(a.opts.Session.AppendMessage(m, usage))
	}
}

func (a *Agent) needsCompaction() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	// With only a prompt and an answer there is nothing worth summarizing;
	// compacting would just throw the conversation away (tiny local windows
	// can be exceeded by the system prompt alone).
	if len(a.messages) < 4 || a.opts.NoAutoCompact || a.opts.Runtime != nil {
		return false
	}
	return a.lastContext > 0 && float64(a.lastContext) > compactThreshold*float64(a.windowLocked())
}

// Compact summarizes the conversation into a single message ("simple
// compaction": nothing from the old transcript is replayed afterwards).
// emit, if set, gets the events along the way (usage, notices, hook
// output) except EvCompacted, whose summary is returned.
func (a *Agent) Compact(ctx context.Context, emit func(Event)) (string, error) {
	var summary string
	err := a.compactWith(ctx, func(e Event) {
		switch {
		case e.Kind == EvCompacted:
			summary = e.Summary
		case emit != nil:
			emit(e)
		}
	}, false, "manual")
	return summary, err
}

func (a *Agent) compact(ctx context.Context, emit func(Event), midTurn bool) error {
	return a.compactWith(ctx, emit, midTurn, "auto")
}

func (a *Agent) compactWith(ctx context.Context, emit func(Event), midTurn bool, trigger string) error {
	a.mu.Lock()
	wholeTurn := a.opts.Runtime != nil
	a.mu.Unlock()
	if wholeTurn {
		return errors.New("compaction is managed by the Claude Code CLI runtime")
	}
	a.runHook(ctx, emit, hooks.Input{HookEventName: hooks.PreCompact, Trigger: trigger}, trigger)
	a.mu.Lock()
	msgs := append([]llm.Message(nil), a.messages...)
	req := llm.Request{
		Model:     a.opts.Model,
		System:    a.opts.System,
		Tools:     a.activeLocked().Specs(), // unchanged tools keep the cached prefix valid
		MaxTokens: 32_000,
		Effort:    a.opts.Effort,
	}
	provider := a.opts.Provider
	pick := a.opts.CompactWith
	tr := a.opts.Trace
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

	reqID := tr.Request(provider.Name(), req, "compaction")
	ctx = tr.Wire(ctx, reqID)
	start := time.Now()
	var final llm.Message
	for ev, err := range provider.Stream(ctx, req) {
		if err != nil {
			tr.Response(reqID, trace.Response{Start: start.UnixMilli(), DurationMS: time.Since(start).Milliseconds(), Error: err.Error()})
			return err
		}
		switch ev.Type {
		case llm.EventNotice:
			emit(Event{Kind: EvNotice, Text: ev.Text})
		case llm.EventDone:
			final = ev.Message
			usage := ev.Usage
			tr.Response(reqID, trace.Response{Message: &final, Usage: &usage, CostUSD: llm.Lookup(req.Model).Cost(usage),
				StopReason: string(ev.StopReason), Start: start.UnixMilli(), DurationMS: time.Since(start).Milliseconds()})
			model := final.Model
			if model == "" {
				model = req.Model
			}
			// The summary replaces the transcript's context rather than
			// joining it, so its spend is recorded on its own.
			a.recordUsage(model, ev.Usage, emit)
			if a.opts.Session != nil {
				a.saveFailed(a.opts.Session.AppendUsage(model, ev.Usage))
			}
		}
	}
	summary := extractSummary(final.Text())
	if summary == "" {
		return errors.New("model returned an empty summary")
	}
	// The task list lives in the transcript being replaced; carry over
	// what is left of it so the model can keep ticking items off.
	if todos, ok := tools.LatestTodos(msgs); ok && !allDone(todos) {
		summary += "\n\nYour task list (todo_write) before this summary:\n" + tools.FormatTodos(todos)
	}
	if midTurn {
		summary += "\n\nContinue the task from where it left off."
	}

	a.mu.Lock()
	a.messages = []llm.Message{session.CompactionMessage(summary, a.SessionPath())}
	a.lastContext = 0
	a.mu.Unlock()
	if a.opts.Session != nil {
		a.saveFailed(a.opts.Session.AppendCompaction(summary))
	}
	tr.Note(trace.KindCompaction, summary)
	emit(Event{Kind: EvCompacted, Summary: summary})
	return nil
}

func allDone(todos []tools.Todo) bool {
	for _, t := range todos {
		if t.Status != tools.TodoCompleted {
			return false
		}
	}
	return true
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
