package llm

import (
	"context"
	"errors"
	"iter"
	"time"
)

type EventType int

const (
	EventTextDelta EventType = iota
	EventThinkingDelta
	EventToolUseStart
	EventDone
	// EventNotice is a message for the user about the request itself, such
	// as a switch to a fallback model. It carries no model output.
	EventNotice
)

// StreamEvent is emitted while a response streams. Deltas are for display
// only; EventDone carries the fully assembled message, which is the source
// of truth for the transcript.
type StreamEvent struct {
	Type EventType
	Text string // delta text, or tool name for EventToolUseStart

	// EventDone only.
	Message    Message
	Usage      Usage
	StopReason StopReason
}

type Provider interface {
	Name() string
	Stream(ctx context.Context, req Request) iter.Seq2[StreamEvent, error]
}

// ErrContextOverflow is wrapped by adapters when the prompt exceeds the
// model's context window, so the agent can compact and retry.
var ErrContextOverflow = errors.New("context window exceeded")

// APIError lets adapters expose retryability without leaking SDK types.
type APIError struct {
	Status    int
	Retryable bool
	// RetryAfter is how long the server asked to wait before retrying
	// (Retry-After, or Gemini's RetryInfo), when it said.
	RetryAfter time.Duration
	Err        error
}

func (e *APIError) Error() string { return e.Err.Error() }
func (e *APIError) Unwrap() error { return e.Err }

// DocumentCapable is implemented by providers whose API accepts a
// BlockDocument: the file is sent whole and the model reads it, rather than
// Larik extracting text and discarding the layout. A provider that does not
// implement this is assumed not to accept documents, so Larik says so
// instead of sending a request the server would reject.
type DocumentCapable interface {
	// AcceptsDocuments reports whether a document of mediaType can be sent.
	AcceptsDocuments(mediaType string) bool
}

// AcceptsDocuments reports whether p can be sent a document of mediaType.
func AcceptsDocuments(p Provider, mediaType string) bool {
	d, ok := p.(DocumentCapable)
	return ok && d.AcceptsDocuments(mediaType)
}

// ModelProber is implemented by providers that can report runtime facts
// about a model, such as a local server's actual context window, which
// may be far smaller than the model supports.
type ModelProber interface {
	// ContextWindow returns the window the server uses for model, or 0.
	ContextWindow(ctx context.Context, model string) int
	// SupportsTools reports whether model can call tools; known is false
	// when the server can't tell.
	SupportsTools(ctx context.Context, model string) (supported, known bool)
}
