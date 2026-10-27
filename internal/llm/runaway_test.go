package llm

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
)

func TestWhitespaceRun(t *testing.T) {
	for _, c := range []struct {
		deltas []string
		want   int
	}{
		{[]string{`{"code":"a`, "  \t\n"}, 4},
		{[]string{`x\n\t \r`}, 7},
		{[]string{`x\`, `n\`, `t`}, 4}, // escapes split across deltas
		{[]string{`   \\n`}, 0},        // an escaped backslash, then a letter
		{[]string{`\n\n`, `b`, ` `}, 1},
	} {
		var w WhitespaceRun
		got := 0
		for _, d := range c.deltas {
			got = w.Feed(d)
		}
		if got != c.want {
			t.Errorf("Feed(%q) = %d, want %d", c.deltas, got, c.want)
		}
	}
}

// scripted streams one prepared answer per call.
type scripted struct {
	answers []func(yield func(StreamEvent, error) bool)
	calls   int
}

func (s *scripted) Name() string { return "scripted" }

func (s *scripted) Stream(context.Context, Request) iter.Seq2[StreamEvent, error] {
	a := s.answers[s.calls]
	s.calls++
	return a
}

func runaway(yield func(StreamEvent, error) bool) {
	if yield(StreamEvent{Type: EventToolUseStart, Text: "run_code"}, nil) {
		yield(StreamEvent{}, &RunawayError{Provider: "scripted", Chars: RunawayLimit + 1})
	}
}

func answer(yield func(StreamEvent, error) bool) {
	yield(StreamEvent{Type: EventDone, Message: Message{Role: RoleAssistant, Blocks: []Block{TextBlock("ok")}}}, nil)
}

func streamAll(p Provider) (notices []string, done bool, err error) {
	for ev, e := range p.Stream(context.Background(), Request{}) {
		if e != nil {
			return notices, done, e
		}
		if ev.Type == EventNotice {
			notices = append(notices, ev.Text)
		}
		done = done || ev.Type == EventDone
	}
	return notices, done, nil
}

func TestRunawayRetriesOnce(t *testing.T) {
	s := &scripted{answers: []func(func(StreamEvent, error) bool){runaway, answer}}
	notices, done, err := streamAll(WithRunawayRetry(s))
	if err != nil || !done || s.calls != 2 || len(notices) != 1 || !strings.Contains(notices[0], "asking again") {
		t.Fatalf("calls %d, notices %q, done %v, err %v", s.calls, notices, done, err)
	}

	s = &scripted{answers: []func(func(StreamEvent, error) bool){runaway, runaway, answer}}
	_, _, err = streamAll(WithRunawayRetry(s))
	var r *RunawayError
	if !errors.As(err, &r) || s.calls != 2 {
		t.Fatalf("a second runaway should end the request: calls %d, err %v", s.calls, err)
	}

	other := func(yield func(StreamEvent, error) bool) { yield(StreamEvent{}, errors.New("boom")) }
	s = &scripted{answers: []func(func(StreamEvent, error) bool){other, answer}}
	if _, _, err = streamAll(WithRunawayRetry(s)); err == nil || s.calls != 1 {
		t.Fatalf("other errors are not retried here: calls %d, err %v", s.calls, err)
	}
}
