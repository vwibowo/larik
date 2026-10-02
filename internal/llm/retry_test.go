package llm

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"testing"
	"time"
)

// failing fails each request with err and counts the attempts.
type failing struct {
	err   error
	tries int
}

func (f *failing) Name() string { return "failing" }
func (f *failing) Stream(context.Context, Request) iter.Seq2[StreamEvent, error] {
	return func(yield func(StreamEvent, error) bool) {
		f.tries++
		yield(StreamEvent{}, f.err)
	}
}

// TestRetryGivesUpOnLongRetryAfter: when the server asks for a wait past
// maxRetryWait, retrying sooner would only be refused again, so the error
// is surfaced at once.
func TestRetryGivesUpOnLongRetryAfter(t *testing.T) {
	f := &failing{err: &APIError{Status: 429, Retryable: true, RetryAfter: 5 * time.Minute, Err: errors.New("rate limited")}}
	start := time.Now()
	for _, err := range WithRetry(f, 4).Stream(context.Background(), Request{}) {
		if err == nil {
			t.Fatal("expected the error")
		}
	}
	if f.tries != 1 || time.Since(start) > time.Second {
		t.Fatalf("tries = %d after %s, want 1 at once", f.tries, time.Since(start))
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := ParseRetryAfter("7"); got != 7*time.Second {
		t.Errorf("seconds: %s", got)
	}
	if got := ParseRetryAfter(time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)); got < 28*time.Second || got > 31*time.Second {
		t.Errorf("date: %s", got)
	}
	for _, h := range []string{"", "soon", "-3"} {
		if got := ParseRetryAfter(h); got != 0 {
			t.Errorf("%q: %s", h, got)
		}
	}
}
