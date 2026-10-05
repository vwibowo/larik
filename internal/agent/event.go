package agent

import (
	"encoding/json"

	"larik/internal/llm"
	"larik/internal/permission"
)

type EventKind string

const (
	EvTextDelta     EventKind = "text_delta"
	EvThinkingDelta EventKind = "thinking_delta"
	EvToolCallDelta EventKind = "tool_call_streaming" // model started emitting a tool call
	EvAssistant     EventKind = "assistant_message"   // final assembled assistant message
	EvToolStart     EventKind = "tool_start"
	EvToolEnd       EventKind = "tool_end"
	EvPermission    EventKind = "permission_request"
	EvUsage         EventKind = "usage"
	EvCompacted     EventKind = "compacted"
	EvNotice        EventKind = "notice"
	EvError         EventKind = "error"
	EvDone          EventKind = "done"
	// EvTaskDone arrives on Agent.Background() when a background task ends:
	// ToolID is the task id, Agent its label, Output the result, StopReason
	// its status.
	EvTaskDone EventKind = "task_done"
)

// Event is the single stream every front end consumes. Fields are set
// according to Kind.
type Event struct {
	Kind EventKind `json:"type"`

	// Agent labels events forwarded from a subagent ("explore: find auth");
	// empty for the main agent.
	Agent string `json:"agent,omitempty"`
	// Model is the model a forwarded subagent event ran on, so a front end
	// can show which tier is doing the work; empty for the main agent.
	Model string `json:"model,omitempty"`

	Text string `json:"text,omitempty"` // deltas, notices, errors

	Message *llm.Message `json:"message,omitempty"` // EvAssistant

	// Tool events.
	ToolID   string          `json:"tool_id,omitempty"`
	ToolName string          `json:"tool_name,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
	Output   string          `json:"output,omitempty"`
	Display  string          `json:"display,omitempty"`
	IsError  bool            `json:"is_error,omitempty"`

	// EvPermission: the front end must send exactly one reply.
	SuggestedRule string `json:"suggested_rule,omitempty"`
	// Cwd is the directory relative paths in Input are relative to: a
	// worktree subagent's, not the project's.
	Cwd string `json:"cwd,omitempty"`
	// AutoReason, in auto mode, is why the call is put to the user: the
	// classifier's reason for not approving it, or that it failed.
	AutoReason string                 `json:"auto_reason,omitempty"`
	Reply      chan<- PermissionReply `json:"-"`

	// EvUsage and EvCompacted.
	Usage      *UsageInfo      `json:"usage,omitempty"`
	Summary    string          `json:"summary,omitempty"` // EvCompacted; retained for protocol compatibility
	Compaction *CompactionInfo `json:"compaction,omitempty"`

	// EvDone.
	StopReason string `json:"stop_reason,omitempty"`
}

type PermissionReply struct {
	Allow     bool
	Always    bool         // persist SuggestedRule
	Reason    string       // optional feedback for the model on deny
	Persisted chan<- error // optional result of persisting an "always allow" answer
	// Mode, for an approved exit_plan_mode, is the permission mode to
	// continue in (default when empty).
	Mode permission.Mode
}

// CompactionInfo reports the provider-grounded size reduction from one
// compaction. BeforeTokens is the summarizer request's prompt size and
// AfterTokens is its billed output size, which can include hidden reasoning.
// The rebuilt context also contains the stable system/tool prefix and a small
// summary wrapper, so SavedTokens is an estimate rather than an exact
// next-request measurement. Available is false when the provider reports no
// usage; in that case the numeric fields are zero and must not be displayed.
type CompactionInfo struct {
	Trigger      string `json:"trigger,omitempty"`
	BeforeTokens int    `json:"before_tokens"`
	AfterTokens  int    `json:"after_tokens"`
	SavedTokens  int    `json:"saved_tokens"`
	Estimated    bool   `json:"estimated"`
	Available    bool   `json:"available"`
}

type UsageInfo struct {
	// Model made the Turn's request; it can differ from the agent's model
	// after a fallback or for compaction.
	Model                  string    `json:"model,omitempty"`
	Turn                   llm.Usage `json:"turn"`
	Total                  llm.Usage `json:"total"`
	CostUSD                float64   `json:"cost_usd"`
	ContextTokens          int       `json:"context_tokens"`
	ContextWindow          int       `json:"context_window"`
	RequestMS              int64     `json:"request_ms,omitempty"`
	TTFTMS                 int64     `json:"ttft_ms,omitempty"`
	Delegated              llm.Usage `json:"delegated,omitempty"`
	DelegatedCostUSD       float64   `json:"delegated_cost_usd,omitempty"`
	DelegatedTasks         int       `json:"delegated_tasks,omitempty"`
	Compactions            int       `json:"compactions,omitempty"`
	CompactionMeasurements int       `json:"compaction_measurements,omitempty"`
	CompactionSavedTokens  int       `json:"compaction_saved_tokens,omitempty"`
}
