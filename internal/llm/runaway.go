package llm

import (
	"context"
	"errors"
	"fmt"
	"iter"
)

// A model sometimes falls into writing whitespace inside a tool call's
// arguments and doesn't stop until its output limit: one run streamed
// 128,000 tokens of spaces, tabs and newlines for 13 minutes. Data keeps
// arriving, so the stall timeout never fires. Adapters count the
// whitespace with a WhitespaceRun and stop such a response with a
// RunawayError, and WithRunawayRetry asks again once.

// RunawayLimit is how many characters of whitespace in a row a tool call's
// arguments may stream. Real calls stay far below it: the longest run in
// 635 calls from comparison runs was 18.
const RunawayLimit = 2000

// RunawayError reports a response stopped because a tool call's arguments
// turned into whitespace only.
type RunawayError struct {
	Provider string
	Chars    int
}

func (e *RunawayError) Error() string {
	return fmt.Sprintf("%s wrote %d characters of nothing but whitespace into a tool call, so the response was stopped", e.Provider, e.Chars)
}

// WhitespaceRun counts the whitespace at the end of streamed JSON text:
// raw, and escaped inside strings (\n, \t, \r), even when an escape is
// split across two deltas.
type WhitespaceRun struct {
	n      int
	escape bool // the last delta ended in a backslash
}

// Feed adds a delta and returns how long the run of whitespace is now.
func (w *WhitespaceRun) Feed(delta string) int {
	for _, r := range delta {
		switch {
		case w.escape:
			w.escape = false
			if r == 'n' || r == 't' || r == 'r' {
				w.n += 2
			} else {
				w.n = 0
			}
		case r == '\\':
			w.escape = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			w.n++
		default:
			w.n = 0
		}
	}
	return w.n
}

// WithRunawayRetry asks again, once, when a response stops with a
// RunawayError, after a notice that says why. What the first response
// streamed was for display only and is dropped.
func WithRunawayRetry(p Provider) Provider { return &runawayRetry{p} }

type runawayRetry struct{ Provider }

func (r *runawayRetry) Stream(ctx context.Context, req Request) iter.Seq2[StreamEvent, error] {
	return func(yield func(StreamEvent, error) bool) {
		for attempt := 0; ; attempt++ {
			var failure error
			for ev, err := range r.Provider.Stream(ctx, req) {
				if err != nil {
					failure = err
					break
				}
				if !yield(ev, nil) {
					return
				}
			}
			if failure == nil {
				return
			}
			var runaway *RunawayError
			if attempt > 0 || !errors.As(failure, &runaway) || ctx.Err() != nil {
				yield(StreamEvent{}, failure)
				return
			}
			if !yield(StreamEvent{Type: EventNotice, Text: failure.Error() + "; asking again"}, nil) {
				return
			}
		}
	}
}

// Unwrap returns the provider being retried.
func (r *runawayRetry) Unwrap() Provider { return r.Provider }

// ContextWindow and SupportsTools pass through to the wrapped provider when
// it is a ModelProber, so wrapping one keeps its probes.
func (r *runawayRetry) ContextWindow(ctx context.Context, model string) int {
	if p, ok := r.Provider.(ModelProber); ok {
		return p.ContextWindow(ctx, model)
	}
	return 0
}

func (r *runawayRetry) SupportsTools(ctx context.Context, model string) (supported, known bool) {
	if p, ok := r.Provider.(ModelProber); ok {
		return p.SupportsTools(ctx, model)
	}
	return false, false
}
