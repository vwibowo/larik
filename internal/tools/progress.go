package tools

import (
	"bytes"
	"context"
	"sync"
	"time"
)

type progressKey struct{}

// WithProgress gives a running tool somewhere to report partial output:
// report gets the latest tail, at most every progressEvery.
func WithProgress(ctx context.Context, report func(tail string)) context.Context {
	return context.WithValue(ctx, progressKey{}, report)
}

func progressOf(ctx context.Context) func(string) {
	f, _ := ctx.Value(progressKey{}).(func(string))
	return f
}

const (
	progressEvery = 250 * time.Millisecond
	progressLines = 8
	progressBytes = 4 << 10
)

// tailWriter keeps the last progressBytes written and reports the last
// progressLines of them, throttled, so a long command shows signs of life.
type tailWriter struct {
	mu     sync.Mutex
	buf    []byte
	last   time.Time
	report func(string)
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > progressBytes {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-progressBytes:]...)
	}
	if now := time.Now(); now.Sub(t.last) >= progressEvery {
		t.last = now
		t.report(lastLines(t.buf, progressLines))
	}
	return len(p), nil
}

// lastLines is the final n complete-or-partial lines of b.
func lastLines(b []byte, n int) string {
	b = bytes.TrimRight(b, "\n")
	i := len(b)
	for ; n > 0 && i > 0; n-- {
		j := bytes.LastIndexByte(b[:i], '\n')
		if j < 0 {
			i = 0
			break
		}
		i = j
	}
	if i > 0 {
		i++ // skip the newline itself
	}
	return string(b[i:])
}
