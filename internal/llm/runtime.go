package llm

import "context"

// AgentRuntime is a provider which owns a complete model/tool turn rather
// than a single API request. Tool requests still return to the Larik agent so
// its permissions, hooks, and execution path remain authoritative.
type AgentRuntime interface {
	Run(context.Context, AgentRuntimeRequest) (<-chan AgentRuntimeEvent, error)
}

type AgentRuntimeRequest struct {
	Model, System, Workspace string
	Messages                 []Message
	Tools                    []ToolSpec
	Effort                   Effort
	MaxTurns                 int
}

type AgentRuntimeToolRequest struct {
	Call   Block
	Result chan<- Block
}

type AgentRuntimeEvent struct {
	Text, Thinking string
	Assistant      *Message
	Tool           *AgentRuntimeToolRequest
	Usage          *Usage
	Done           bool
	Err            error
}
