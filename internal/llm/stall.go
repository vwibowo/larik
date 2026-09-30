package llm

import (
	"context"
	"fmt"
	"io"
	"iter"
	"net/http"
	"sync/atomic"
	"time"
)

// WithStallTimeout stops a model request whose server has sent nothing for
// d: no response at all, or a stream that went quiet partway. The request
// fails with a StallError, which WithFallback treats like any other
// timeout. Without it, a server that accepts a request and never answers
// hangs the turn until the user interrupts it.
//
// The time is measured in bytes on the wire, so a provider's keep-alives
// count as activity and a model that is quietly thinking isn't cut off as
// long as its server says so. A d of zero or less returns p unchanged.
func WithStallTimeout(p Provider, d time.Duration) Provider {
	if d <= 0 {
		return p
	}
	return &stallGuard{p, d}
}

// StallError reports a model request stopped by WithStallTimeout.
type StallError struct {
	Provider string
	After    time.Duration
}

func (e *StallError) Error() string {
	return fmt.Sprintf("%s sent nothing for %s, so the request was stopped (see stall_timeout)", e.Provider, e.After)
}

// Timeout and Temporary make it a net.Error: a timeout, to the code that
// decides whether to switch to a fallback model.
func (e *StallError) Timeout() bool   { return true }
func (e *StallError) Temporary() bool { return true }

type stallGuard struct {
	Provider
	d time.Duration
}

// stallWatch is one Stream call's timeout, carried in the context to the
// transport that enforces it.
type stallWatch struct {
	d       time.Duration
	tripped atomic.Bool
}

type stallKey struct{}

func (s *stallGuard) Stream(ctx context.Context, req Request) iter.Seq2[StreamEvent, error] {
	return func(yield func(StreamEvent, error) bool) {
		w := &stallWatch{d: s.d}
		finished := false
		for ev, err := range s.Provider.Stream(context.WithValue(ctx, stallKey{}, w), req) {
			// The adapter sees a stall as its request being cancelled;
			// say what actually happened instead.
			if err != nil && w.tripped.Load() && ctx.Err() == nil {
				err = &StallError{Provider: s.Name(), After: s.d}
			}
			finished = finished || err != nil || ev.Type == EventDone
			if !yield(ev, err) {
				return
			}
		}
		// An adapter may end quietly when its request is cancelled.
		if !finished && w.tripped.Load() && ctx.Err() == nil {
			yield(StreamEvent{}, &StallError{Provider: s.Name(), After: s.d})
		}
	}
}

// Unwrap returns the provider being watched.
func (s *stallGuard) Unwrap() Provider { return s.Provider }

// ContextWindow and SupportsTools pass through to the wrapped provider when
// it is a ModelProber, so wrapping one keeps its probes.
func (s *stallGuard) ContextWindow(ctx context.Context, model string) int {
	if p, ok := s.Provider.(ModelProber); ok {
		return p.ContextWindow(ctx, model)
	}
	return 0
}

func (s *stallGuard) SupportsTools(ctx context.Context, model string) (supported, known bool) {
	if p, ok := s.Provider.(ModelProber); ok {
		return p.SupportsTools(ctx, model)
	}
	return false, false
}

// stallTransport enforces the stallWatch in a request's context. Requests
// without one pass straight through.
type stallTransport struct{ base http.RoundTripper }

func (t stallTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w, _ := r.Context().Value(stallKey{}).(*stallWatch)
	if w == nil {
		return t.base.RoundTrip(r)
	}
	if w.tripped.Load() {
		// The SDKs retry a failed connection. After a stall that would
		// mean waiting the whole timeout again, several times over.
		return nil, context.DeadlineExceeded
	}
	ctx, cancel := context.WithCancel(r.Context())
	timer := time.AfterFunc(w.d, func() {
		w.tripped.Store(true)
		cancel()
	})
	resp, err := t.base.RoundTrip(r.WithContext(ctx))
	timer.Stop()
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &stallBody{ReadCloser: resp.Body, timer: timer, d: w.d, cancel: cancel}
	return resp, nil
}

// stallBody runs the timer while a Read is waiting for the server, and not
// while the caller is busy with what it has already read.
type stallBody struct {
	io.ReadCloser
	timer  *time.Timer
	d      time.Duration
	cancel context.CancelFunc
}

func (b *stallBody) Read(p []byte) (int, error) {
	b.timer.Reset(b.d)
	n, err := b.ReadCloser.Read(p)
	b.timer.Stop()
	return n, err
}

func (b *stallBody) Close() error {
	b.timer.Stop()
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}
