package claudeagent

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"larik/internal/llm"
)

// runtimeActivity tracks bytes read and all outstanding bridge handlers,
// including handlers blocked delivering a request or awaiting permission.
// No lock is held while delivering events, executing tools, or canceling a child.
type runtimeActivity struct {
	mu              sync.Mutex
	timeout         time.Duration
	last            time.Time
	tools           int
	warned, expired bool
	wake            chan struct{}
}

func newRuntimeActivity(timeout time.Duration) *runtimeActivity {
	return &runtimeActivity{timeout: timeout, last: time.Now(), wake: make(chan struct{}, 1)}
}

func (a *runtimeActivity) changed() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *runtimeActivity) touch() {
	a.mu.Lock()
	a.last, a.warned = time.Now(), false
	a.mu.Unlock()
	a.changed()
}

func (a *runtimeActivity) toolStarted() {
	a.mu.Lock()
	a.tools++
	a.warned = false
	a.mu.Unlock()
	a.changed()
}

func (a *runtimeActivity) toolFinished() {
	a.mu.Lock()
	a.tools--
	// Resume with a fresh allowance only when the final parallel call ends.
	if a.tools == 0 {
		a.last, a.warned = time.Now(), false
	}
	a.mu.Unlock()
	a.changed()
}

func (a *runtimeActivity) timedOut() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.expired
}

func (a *runtimeActivity) timeoutError() error {
	return fmt.Errorf("Claude CLI stalled: no stream activity for %s (stall_timeout); turn stopped without retrying", a.timeout)
}

// watch returns an idempotent stop-and-join function. The timer remains live
// even if the event consumer is slow; warnings must never prevent cancellation.
func (a *runtimeActivity) watch(ctx context.Context, cancel context.CancelFunc, out chan<- llm.AgentRuntimeEvent) func() {
	if a.timeout <= 0 {
		return func() {}
	}
	stop, done := make(chan struct{}), make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		timer := time.NewTimer(a.timeout)
		defer timer.Stop()
		warningAfter := min(time.Minute, a.timeout/2)
		for {
			if ctx.Err() != nil {
				return
			}
			a.mu.Lock()
			last := a.last
			elapsed := time.Since(last)
			paused := a.tools > 0
			expired := !paused && elapsed >= a.timeout
			if expired {
				a.expired = true
			}
			var notice llm.AgentRuntimeEvent
			var notices chan<- llm.AgentRuntimeEvent
			delay := a.timeout - elapsed
			if !paused && !a.warned {
				if elapsed >= warningAfter {
					notice.Notice = fmt.Sprintf("Claude CLI has had no stream activity for %s; stopping after %s of inactivity unless activity resumes.", elapsed.Round(time.Millisecond), a.timeout)
					notices = out
				} else {
					delay = warningAfter - elapsed
				}
			}
			a.mu.Unlock()
			if expired {
				cancel()
				return
			}
			if paused {
				delay = a.timeout
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(delay)
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-a.wake:
			case <-timer.C:
			case notices <- notice:
				a.mu.Lock()
				if a.last == last && a.tools == 0 {
					a.warned = true
				}
				a.mu.Unlock()
			}
		}
	}()
	return func() { once.Do(func() { close(stop) }); <-done }
}

// Read-level activity includes partial JSON lines and non-content protocol
// messages, rather than only user-visible text or complete scanner tokens.
type activityReader struct {
	io.Reader
	activity *runtimeActivity
}

func (r activityReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 {
		r.activity.touch()
	}
	return n, err
}
