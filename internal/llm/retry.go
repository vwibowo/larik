package llm

import (
	"context"
	"errors"
	"iter"
	"math/rand/v2"
	"time"
)

// WithRetry retries a stream that fails before yielding anything. Once a
// delta has reached the caller, a mid-stream failure is surfaced as-is
// because the partial output can't be un-shown.
func WithRetry(p Provider, attempts int) Provider { return &retrying{p, attempts} }

type retrying struct {
	Provider
	attempts int
}

func (r *retrying) Stream(ctx context.Context, req Request) iter.Seq2[StreamEvent, error] {
	return func(yield func(StreamEvent, error) bool) {
		for attempt := 0; ; attempt++ {
			started := false
			var failure error
			for ev, err := range r.Provider.Stream(ctx, req) {
				if err != nil {
					failure = err
					break
				}
				started = true
				if !yield(ev, nil) {
					return
				}
			}
			if failure == nil {
				return
			}
			if started || attempt+1 >= r.attempts || !Retryable(failure) || ctx.Err() != nil {
				yield(StreamEvent{}, failure)
				return
			}
			backoff := time.Duration(1<<attempt)*time.Second + time.Duration(rand.IntN(500))*time.Millisecond
			select {
			case <-ctx.Done():
				yield(StreamEvent{}, ctx.Err())
				return
			case <-time.After(backoff):
			}
		}
	}
}

func Retryable(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable
	}
	return false
}

// ClassifyStatus builds an APIError from an HTTP status code.
func ClassifyStatus(status int, err error) error {
	return &APIError{
		Status:    status,
		Retryable: status == 408 || status == 409 || status == 429 || status >= 500,
		Err:       err,
	}
}

// ReplayableBlocks returns the blocks of m that may be sent to provider/model.
// Thinking and opaque blocks are bound to the producer; other providers get
// only the portable blocks.
func ReplayableBlocks(m Message, provider, model string) []Block {
	sameProducer := m.Model == model
	out := make([]Block, 0, len(m.Blocks))
	for _, b := range m.Blocks {
		switch b.Type {
		case BlockThinking, BlockOpaque:
			if !sameProducer || b.Provider != provider {
				continue
			}
		}
		out = append(out, b)
	}
	return out
}
