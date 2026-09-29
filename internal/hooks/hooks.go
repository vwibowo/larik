// Package hooks runs user-configured hooks at points in the agent
// lifecycle. The configuration, stdin payload, exit codes and JSON output
// follow the Claude Code hooks format so existing hook scripts work.
//
// A command hook is a shell command:
// exit code 0: success; stdout may be a JSON object (see output).
// Exit code 2: blocking; stderr is the reason, routed per event.
// Other codes: non-blocking error, shown to the user.
//
// A prompt hook asks a model instead (see prompt.go): {"ok": false,
// "reason": …} blocks like exit code 2.
package hooks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"larik/internal/procgroup"
)

type Event string

const (
	SessionStart     Event = "SessionStart"
	UserPromptSubmit Event = "UserPromptSubmit"
	PreToolUse       Event = "PreToolUse"
	PostToolUse      Event = "PostToolUse"
	Stop             Event = "Stop"
	SubagentStop     Event = "SubagentStop"
	PreCompact       Event = "PreCompact"
	Notification     Event = "Notification"
	SessionEnd       Event = "SessionEnd"
)

// Events lists every supported event, in lifecycle order.
var Events = []Event{SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, Stop, SubagentStop, PreCompact, Notification, SessionEnd}

const defaultTimeout = 60 * time.Second

type Command struct {
	Type    string `json:"type"` // "command" (default) or "prompt"
	Command string `json:"command,omitempty"`
	// Prompt is a prompt hook's question for the model; $ARGUMENTS is
	// replaced with the hook input as JSON (appended when absent).
	Prompt string `json:"prompt,omitempty"`
	// Model is a prompt hook's provider/model or routing role; empty uses
	// the explore role, else the session's model.
	Model   string `json:"model,omitempty"`
	Timeout int    `json:"timeout,omitempty"` // seconds
}

type Matcher struct {
	// Matcher is a regex matched against the tool name (tool events) or the
	// source/trigger (SessionStart, PreCompact). Empty or "*" matches all.
	Matcher string    `json:"matcher,omitempty"`
	Hooks   []Command `json:"hooks"`
}

// Config maps events to matchers, as in settings files.
type Config map[Event][]Matcher

// Merge appends other's matchers to c.
func (c Config) Merge(other Config) Config {
	out := Config{}
	for _, src := range []Config{c, other} {
		for ev, ms := range src {
			out[ev] = append(out[ev], ms...)
		}
	}
	return out
}

func (c Config) Empty() bool {
	for _, ms := range c {
		for _, m := range ms {
			if len(m.Hooks) > 0 {
				return false
			}
		}
	}
	return true
}

// Hash identifies a hook set so approvals stop applying when it changes.
func (c Config) Hash() string {
	if c.Empty() {
		return ""
	}
	evs := make([]string, 0, len(c))
	for ev := range c {
		evs = append(evs, string(ev))
	}
	sort.Strings(evs)
	h := sha256.New()
	for _, ev := range evs {
		b, _ := json.Marshal(c[Event(ev)])
		fmt.Fprintf(h, "%s=%s\n", ev, b)
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// Input is the JSON payload written to the hook's stdin.
type Input struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	HookEventName  Event           `json:"hook_event_name"`
	PermissionMode string          `json:"permission_mode,omitempty"`
	ToolName       string          `json:"tool_name,omitempty"`
	ToolInput      json.RawMessage `json:"tool_input,omitempty"`
	ToolUseID      string          `json:"tool_use_id,omitempty"`
	ToolResponse   *ToolResponse   `json:"tool_response,omitempty"`
	Prompt         string          `json:"prompt,omitempty"`
	StopHookActive bool            `json:"stop_hook_active,omitempty"`
	Source         string          `json:"source,omitempty"`     // SessionStart: startup, resume, clear
	Trigger        string          `json:"trigger,omitempty"`    // PreCompact: manual, auto
	Message        string          `json:"message,omitempty"`    // Notification
	Reason         string          `json:"reason,omitempty"`     // SessionEnd
	AgentType      string          `json:"agent_type,omitempty"` // set inside subagents
}

type ToolResponse struct {
	Output  string `json:"output"`
	IsError bool   `json:"is_error"`
}

// output is the optional JSON a hook prints on stdout with exit code 0.
type output struct {
	Continue      *bool  `json:"continue"`
	StopReason    string `json:"stopReason"`
	SystemMessage string `json:"systemMessage"`
	Decision      string `json:"decision"` // "block" (or legacy "approve")
	Reason        string `json:"reason"`
	HookSpecific  struct {
		PermissionDecision       string          `json:"permissionDecision"` // allow, deny, ask
		PermissionDecisionReason string          `json:"permissionDecisionReason"`
		UpdatedInput             json.RawMessage `json:"updatedInput"`
		AdditionalContext        string          `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// Result aggregates every hook that ran for one event.
type Result struct {
	// Block: exit code 2, decision "block", or permissionDecision "deny".
	Block  bool
	Reason string
	// Permission is "allow", "deny", "ask" or "" (PreToolUse).
	Permission   string
	UpdatedInput json.RawMessage
	// Context is extra text for the model.
	Context []string
	// Messages are shown to the user (systemMessage, hook errors).
	Messages []string
	// Halt: a hook returned continue:false; the turn should end.
	Halt       bool
	HaltReason string
}

// Runner executes hooks. A nil *Runner runs nothing.
type Runner struct {
	cwd, sessionID, transcript string

	mu  sync.Mutex
	cfg Config
	// evaluate answers prompt hooks (SetEvaluator); nil makes them fail
	// open with a message.
	evaluate Evaluator
}

func NewRunner(cfg Config, cwd, sessionID, transcriptPath string) *Runner {
	return &Runner{cfg: cfg, cwd: cwd, sessionID: sessionID, transcript: transcriptPath}
}

// SetConfig replaces the active hooks (e.g. after approving project hooks).
// pipeWaitDelay is how long a finished hook's output may stay open, held by
// a process it left running, before Larik stops reading it.
const pipeWaitDelay = 2 * time.Second

func (r *Runner) SetConfig(cfg Config) {
	r.mu.Lock()
	r.cfg = cfg
	r.mu.Unlock()
}

// Has reports whether any hook is configured for ev.
func (r *Runner) Has(ev Event) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.cfg[ev]) > 0
}

// Run executes the hooks for in.HookEventName whose matcher accepts target,
// in parallel, and aggregates their results.
func (r *Runner) Run(ctx context.Context, in Input, target string) Result {
	if r == nil {
		return Result{}
	}
	r.mu.Lock()
	var cmds []Command
	for _, m := range r.cfg[in.HookEventName] {
		if matches(m.Matcher, target) {
			cmds = append(cmds, m.Hooks...)
		}
	}
	r.mu.Unlock()
	if len(cmds) == 0 {
		return Result{}
	}

	in.SessionID, in.TranscriptPath, in.Cwd = r.sessionID, r.transcript, r.cwd
	payload, _ := json.Marshal(in)

	results := make([]Result, len(cmds))
	var wg sync.WaitGroup
	for i, c := range cmds {
		wg.Add(1)
		go func(i int, c Command) {
			defer wg.Done()
			results[i] = r.exec(ctx, in.HookEventName, c, payload)
		}(i, c)
	}
	wg.Wait()
	return combine(results)
}

func matches(pattern, target string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	re, err := regexp.Compile("(?i)^(?:" + pattern + ")$")
	if err != nil {
		return pattern == target
	}
	return re.MatchString(target)
}

func (r *Runner) exec(ctx context.Context, ev Event, c Command, payload []byte) Result {
	if c.Type == "prompt" {
		return r.evalPrompt(ctx, ev, c, payload)
	}
	if c.Type != "" && c.Type != "command" {
		return Result{Messages: []string{fmt.Sprintf("%s hook: unsupported type %q", ev, c.Type)}}
	}
	timeout := defaultTimeout
	if c.Timeout > 0 {
		timeout = time.Duration(c.Timeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.Command("bash", "-c", c.Command)
	cmd.Dir = r.cwd
	cmd.Env = append(os.Environ(), "LARIK_PROJECT_DIR="+r.cwd, "CLAUDE_PROJECT_DIR="+r.cwd)
	cmd.Stdin = bytes.NewReader(payload)
	procgroup.Configure(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// A process the hook leaves running mustn't hold the agent up.
	cmd.WaitDelay = pipeWaitDelay
	if err := cmd.Start(); err != nil {
		return Result{Messages: []string{fmt.Sprintf("%s hook failed to start: %v", ev, err)}}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = procgroup.Kill(cmd)
		<-done
		return Result{Messages: []string{fmt.Sprintf("%s hook timed out after %s: %s", ev, timeout, short(c.Command))}}
	}

	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil // the hook exited; something it started kept its output open
	}
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		return Result{Messages: []string{fmt.Sprintf("%s hook error: %v", ev, err)}}
	}
	errText := strings.TrimSpace(stderr.String())
	switch code {
	case 0:
		return parseSuccess(ev, strings.TrimSpace(stdout.String()))
	case 2:
		if errText == "" {
			errText = "blocked by " + string(ev) + " hook"
		}
		res := Result{Block: true, Reason: errText}
		if ev == PreToolUse {
			res.Permission = "deny"
		}
		return res
	default:
		msg := fmt.Sprintf("%s hook exited %d: %s", ev, code, short(c.Command))
		if errText != "" {
			msg += ": " + errText
		}
		return Result{Messages: []string{msg}}
	}
}

func parseSuccess(ev Event, stdout string) Result {
	var res Result
	var out output
	if strings.HasPrefix(stdout, "{") && json.Unmarshal([]byte(stdout), &out) == nil {
		if out.Continue != nil && !*out.Continue {
			res.Halt, res.HaltReason = true, out.StopReason
		}
		if out.SystemMessage != "" {
			res.Messages = append(res.Messages, out.SystemMessage)
		}
		if out.Decision == "block" {
			res.Block, res.Reason = true, out.Reason
		}
		hs := out.HookSpecific
		switch hs.PermissionDecision {
		case "allow", "deny", "ask":
			res.Permission = hs.PermissionDecision
			if hs.PermissionDecision == "deny" {
				res.Block, res.Reason = true, hs.PermissionDecisionReason
			} else if hs.PermissionDecisionReason != "" {
				res.Reason = hs.PermissionDecisionReason
			}
		}
		if out.Decision == "approve" && res.Permission == "" { // legacy spelling
			res.Permission = "allow"
		}
		if len(hs.UpdatedInput) > 0 && string(hs.UpdatedInput) != "null" {
			res.UpdatedInput = hs.UpdatedInput
		}
		if hs.AdditionalContext != "" {
			res.Context = append(res.Context, hs.AdditionalContext)
		}
		return res
	}
	// Plain stdout becomes model context where that is meaningful.
	if stdout != "" && (ev == UserPromptSubmit || ev == SessionStart) {
		res.Context = append(res.Context, stdout)
	}
	return res
}

// combine merges results: deny beats ask beats allow; any block blocks.
func combine(rs []Result) Result {
	var out Result
	rank := map[string]int{"": 0, "allow": 1, "ask": 2, "deny": 3}
	var reasons []string
	for _, r := range rs {
		if r.Block {
			out.Block = true
		}
		if r.Reason != "" {
			reasons = append(reasons, r.Reason)
		}
		if rank[r.Permission] > rank[out.Permission] {
			out.Permission = r.Permission
		}
		if r.UpdatedInput != nil {
			out.UpdatedInput = r.UpdatedInput
		}
		out.Context = append(out.Context, r.Context...)
		out.Messages = append(out.Messages, r.Messages...)
		if r.Halt {
			out.Halt = true
			if r.HaltReason != "" {
				out.HaltReason = r.HaltReason
			}
		}
	}
	out.Reason = strings.Join(reasons, "\n")
	return out
}

func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return s
}
