package agent

import (
	"encoding/json"

	"larik/internal/llm"
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
	SuggestedRule string                 `json:"suggested_rule,omitempty"`
	Reply         chan<- PermissionReply `json:"-"`

	// EvUsage.
	Usage   *UsageInfo `json:"usage,omitempty"`
	Summary string     `json:"summary,omitempty"` // EvCompacted

	// EvDone.
	StopReason string `json:"stop_reason,omitempty"`
}

type PermissionReply struct {
	Allow  bool
	Always bool   // persist SuggestedRule
	Reason string // optional feedback for the model on deny
}

type UsageInfo struct {
	Turn          llm.Usage `json:"turn"`
	Total         llm.Usage `json:"total"`
	CostUSD       float64   `json:"cost_usd"`
	ContextTokens int       `json:"context_tokens"`
	ContextWindow int       `json:"context_window"`
}
