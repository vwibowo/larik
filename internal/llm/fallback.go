package llm

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net"
	"sync"
	"time"
)

// Candidate is a provider and the model id to ask it for.
type Candidate struct {
	Provider Provider
	Model    string
}

func (c Candidate) String() string { return c.Provider.Name() + "/" + c.Model }

// WithFallback tries each candidate in turn when the previous one fails
// before yielding anything with an error worth switching for: a rate
// limit, an exhausted quota, an outage. Like WithRetry it never switches
// once output has reached the caller. The first candidate is the primary;
// requests name its model, and the model is rewritten for the others, so
// the final message records the model that actually answered. Each switch
// is announced with an EventNotice. A candidate that failed is skipped for
// a while (see cooldown) so every request doesn't pay for the same failure.
func WithFallback(primary Candidate, others ...Candidate) Provider {
	if len(others) == 0 {
		return primary.Provider
	}
	return &fallback{chain: append([]Candidate{primary}, others...)}
}

type fallback struct {
	chain []Candidate
	now   func() time.Time // for tests
}

// resting maps a failed "provider/model" to when to try it again. It is
// shared by every chain, since subagents resolve a new one per task.
var (
	restMu    sync.Mutex
	restUntil = map[string]time.Time{}
)

func (f *fallback) Name() string { return f.chain[0].Provider.Name() }

func (f *fallback) clock() time.Time {
	if f.now != nil {
		return f.now()
	}
	return time.Now()
}

// resting reports a candidate still cooling down after a failure. The
// last candidate is always tried, so there is something to answer.
func (f *fallback) resting(i int) bool {
	if i == len(f.chain)-1 {
		return false
	}
	restMu.Lock()
	defer restMu.Unlock()
	return f.clock().Before(restUntil[f.chain[i].String()])
}

func (f *fallback) rest(i int, err error) {
	restMu.Lock()
	restUntil[f.chain[i].String()] = f.clock().Add(cooldown(err))
	restMu.Unlock()
}

// cooldown is how long to leave a failed candidate alone: briefly for
// rate limits and server trouble, longer for problems that take a person
// to fix (a missing model, a bad key, no credit, a stopped server).
func cooldown(err error) time.Duration {
	var apiErr *APIError
	if errors.As(err, &apiErr) && (apiErr.Status == 429 || apiErr.Status == 408 || apiErr.Status >= 500) {
		return time.Minute
	}
	return 10 * time.Minute
}

func (f *fallback) Stream(ctx context.Context, req Request) iter.Seq2[StreamEvent, error] {
	return func(yield func(StreamEvent, error) bool) {
		for i, c := range f.chain {
			if f.resting(i) {
				continue // its failure was announced when it happened
			}
			r := req
			if i > 0 {
				r.Model = c.Model
			}
			started := false
			var failure error
			for ev, err := range c.Provider.Stream(ctx, r) {
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
			next := -1 // the candidate that will actually be tried next
			for j := i + 1; j < len(f.chain) && next < 0; j++ {
				if !f.resting(j) {
					next = j
				}
			}
			if started || next < 0 || !ShouldFallBack(failure) || ctx.Err() != nil {
				yield(StreamEvent{}, failure)
				return
			}
			f.rest(i, failure)
			if !yield(StreamEvent{Type: EventNotice, Text: fmt.Sprintf("%s failed (%s); switched to %s", c, briefError(failure), f.chain[next])}, nil) {
				return
			}
		}
	}
}

// ShouldFallBack reports errors another provider might not have: rate
// limits, quota or billing problems, server errors, timeouts and servers
// that can't be reached. A bad request or an overlong prompt would fail
// the same way elsewhere.
func ShouldFallBack(err error) bool {
	if errors.Is(err, ErrContextOverflow) {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch s := apiErr.Status; {
		case s == 401, s == 402, s == 403, s == 404, s == 408, s == 429, s >= 500:
			return true
		}
		return apiErr.Retryable
	}
	// Can't connect, or timed out: a stopped local server, a network
	// problem. The caller has already ruled out its own cancellation.
	var netErr net.Error
	return errors.As(err, &netErr)
}

func briefError(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status != 0 {
		return fmt.Sprintf("HTTP %d", apiErr.Status)
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "timed out"
		}
		return "unreachable"
	}
	s := err.Error()
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

// ContextWindow and SupportsTools ask whichever candidate serves model, so
// wrapping a local server keeps its runtime facts visible.
func (f *fallback) ContextWindow(ctx context.Context, model string) int {
	if p, ok := f.prober(model); ok {
		return p.ContextWindow(ctx, model)
	}
	return 0
}

func (f *fallback) SupportsTools(ctx context.Context, model string) (bool, bool) {
	if p, ok := f.prober(model); ok {
		return p.SupportsTools(ctx, model)
	}
	return false, false
}

func (f *fallback) prober(model string) (ModelProber, bool) {
	for _, c := range f.chain {
		if c.Model == model {
			p, ok := c.Provider.(ModelProber)
			return p, ok
		}
	}
	return nil, false
}
