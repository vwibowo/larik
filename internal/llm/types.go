// Package llm defines the provider-neutral conversation model used by Larik.
// Provider adapters translate to and from these types; nothing in this
// package imports a vendor SDK.
package llm

import "encoding/json"

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type BlockType string

const (
	BlockText       BlockType = "text"
	BlockThinking   BlockType = "thinking"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
	BlockImage      BlockType = "image"
	// BlockOpaque carries provider-specific items (e.g. OpenAI reasoning items)
	// that must be replayed verbatim to the same provider and dropped elsewhere.
	BlockOpaque BlockType = "opaque"
)

// Block is a flat union so it round-trips through JSONL without custom codecs.
type Block struct {
	Type BlockType `json:"type"`

	// text / thinking
	Text string `json:"text,omitempty"`
	// Signature authenticates thinking (Anthropic) or tool calls (Gemini).
	Signature string `json:"signature,omitempty"`
	// Redacted marks encrypted thinking whose payload lives in Signature.
	Redacted bool `json:"redacted,omitempty"`
	// DurationMS is how long the model thought, for display; adapters
	// don't send it.
	DurationMS int64 `json:"duration_ms,omitempty"`

	// tool_use / tool_result
	ID      string          `json:"id,omitempty"`
	Name    string          `json:"name,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	Content string          `json:"content,omitempty"`
	IsError bool            `json:"is_error,omitempty"`

	// image
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"` // base64

	// Provider owns Signature/Raw; other providers ignore them.
	Provider string          `json:"provider,omitempty"`
	Raw      json.RawMessage `json:"raw,omitempty"`

	// Attachment names the source (a path, or "!command") of a text or
	// image block Larik attached to a prompt. Adapters send such blocks
	// like any other; Text skips them so the prompt shows as typed.
	Attachment string `json:"attachment,omitempty"`
}

type Message struct {
	Role   Role    `json:"role"`
	Blocks []Block `json:"blocks"`
	// Model that produced an assistant message. Thinking/opaque blocks are
	// replayed only to the same provider and model.
	Model string `json:"model,omitempty"`
}

func TextBlock(s string) Block { return Block{Type: BlockText, Text: s} }

func UserText(s string) Message {
	return Message{Role: RoleUser, Blocks: []Block{TextBlock(s)}}
}

// Text concatenates the visible text blocks of a message, leaving out
// attachments.
func (m Message) Text() string {
	var s string
	for _, b := range m.Blocks {
		if b.Type == BlockText && b.Attachment == "" {
			s += b.Text
		}
	}
	return s
}

// ToolUses returns the tool calls requested in the message.
func (m Message) ToolUses() []Block {
	var out []Block
	for _, b := range m.Blocks {
		if b.Type == BlockToolUse {
			out = append(out, b)
		}
	}
	return out
}

type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"` // JSON Schema object
}

// Effort controls reasoning depth. Empty means the provider default.
type Effort string

const (
	EffortDefault Effort = ""
	EffortLow     Effort = "low"
	EffortMedium  Effort = "medium"
	EffortHigh    Effort = "high"
	EffortXHigh   Effort = "xhigh"
	EffortMax     Effort = "max"
)

type Request struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []ToolSpec
	MaxTokens int
	Effort    Effort
}

type Usage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
}

// ContextTokens is the prompt size the model saw on this call.
func (u Usage) ContextTokens() int { return u.Input + u.CacheRead + u.CacheWrite }

func (u *Usage) Add(o Usage) {
	u.Input += o.Input
	u.Output += o.Output
	u.CacheRead += o.CacheRead
	u.CacheWrite += o.CacheWrite
}

type StopReason string

const (
	StopEnd       StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopRefusal   StopReason = "refusal"
	StopOther     StopReason = "other"
)

// Append adds m to msgs, merging consecutive user messages (e.g. a prompt
// typed after an interrupted tool round) so roles keep alternating.
func Append(msgs []Message, m Message) []Message {
	if n := len(msgs); n > 0 && m.Role == RoleUser && msgs[n-1].Role == RoleUser {
		last := msgs[n-1]
		last.Blocks = append(append([]Block(nil), last.Blocks...), m.Blocks...)
		return append(msgs[:n-1:n-1], last)
	}
	return append(msgs, m)
}
