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
	// Parallel names the tools that may run at the same time as each
	// other (read-only or concurrency-safe); the runtime may issue those
	// concurrently and the agent runs them together.
	Parallel []string
	Effort   Effort
	MaxTurns int
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
	// ResolvedModel is the concrete model id the runtime reports for this
	// turn, when it resolves a rolling alias such as "sonnet" to one. It is
	// metadata for looking up limits only: the request's model stays the
	// name the user chose, so usage attribution and replayable-block
	// matching are unaffected.
	ResolvedModel string
	Done          bool
	Err           error
}
