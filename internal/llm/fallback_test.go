package llm

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net"
	"strings"
	"testing"
	"time"
)

// stub fails with err before any output, or after one delta when late is
// set, or else answers; it records the models it was asked for.
type stub struct {
	name   string
	err    error
	late   bool
	models []string
}

func (s *stub) Name() string { return s.name }

func (s *stub) Stream(_ context.Context, req Request) iter.Seq2[StreamEvent, error] {
	return func(yield func(StreamEvent, error) bool) {
		s.models = append(s.models, req.Model)
		if s.err != nil && !s.late {
			yield(StreamEvent{}, s.err)
			return
		}
		if !yield(StreamEvent{Type: EventTextDelta, Text: "hi"}, nil) {
			return
		}
		if s.err != nil {
			yield(StreamEvent{}, s.err)
			return
		}
		yield(StreamEvent{Type: EventDone, Message: Message{Role: RoleAssistant, Model: req.Model, Blocks: []Block{TextBlock("hi from " + s.name)}}}, nil)
	}
}

// fresh forgets which candidates are resting, so tests don't leak into
// each other.
func fresh(t *testing.T) {
	restMu.Lock()
	clear(restUntil)
	restMu.Unlock()
	t.Cleanup(func() {
		restMu.Lock()
		clear(restUntil)
		restMu.Unlock()
	})
}

func collect(p Provider, model string) (events []StreamEvent, err error) {
	for ev, e := range p.Stream(context.Background(), Request{Model: model}) {
		if e != nil {
			return events, e
		}
		events = append(events, ev)
	}
	return events, nil
}

func TestFallbackSwitchesBeforeOutput(t *testing.T) {
	fresh(t)
	primary := &stub{name: "groq", err: ClassifyStatus(429, errors.New("rate limited"))}
	backup := &stub{name: "ollama"}
	p := WithFallback(Candidate{primary, "llama-4-scout"}, Candidate{backup, "qwen3-coder"})
	if p.Name() != "groq" {
		t.Errorf("Name = %q, want the primary's", p.Name())
	}
	evs, err := collect(p, "llama-4-scout")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 || evs[0].Type != EventNotice || !strings.Contains(evs[0].Text, "HTTP 429") || !strings.Contains(evs[0].Text, "ollama/qwen3-coder") {
		t.Fatalf("events = %+v", evs)
	}
	done := evs[len(evs)-1]
	if done.Type != EventDone || done.Message.Model != "qwen3-coder" {
		t.Errorf("final message model = %q, want the fallback's", done.Message.Model)
	}
	if fmt.Sprint(backup.models) != "[qwen3-coder]" {
		t.Errorf("backup asked for %v", backup.models)
	}
}

func TestFallbackNeverSwitchesMidStream(t *testing.T) {
	fresh(t)
	primary := &stub{name: "a", err: ClassifyStatus(503, errors.New("overloaded")), late: true}
	backup := &stub{name: "b"}
	_, err := collect(WithFallback(Candidate{primary, "x"}, Candidate{backup, "y"}), "x")
	if err == nil || len(backup.models) != 0 {
		t.Errorf("err = %v, backup calls = %v; want the error surfaced and no switch", err, backup.models)
	}
}

func TestFallbackSkipsErrorsEveryProviderWouldHit(t *testing.T) {
	fresh(t)
	for _, err := range []error{
		ClassifyStatus(400, errors.New("bad request")),
		fmt.Errorf("too long: %w", ErrContextOverflow),
		errors.New("stream ended"),
	} {
		backup := &stub{name: "b"}
		if _, got := collect(WithFallback(Candidate{&stub{name: "a", err: err}, "x"}, Candidate{backup, "y"}), "x"); got == nil || len(backup.models) != 0 {
			t.Errorf("%v: err = %v, backup calls = %v", err, got, backup.models)
		}
	}
}

func TestFallbackChainExhausted(t *testing.T) {
	fresh(t)
	quota := ClassifyStatus(402, errors.New("out of credit"))
	_, err := collect(WithFallback(Candidate{&stub{name: "a", err: quota}, "x"}, Candidate{&stub{name: "b", err: quota}, "y"}), "x")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 402 {
		t.Errorf("err = %v, want the last provider's error", err)
	}
}

func TestWithFallbackAloneIsThePrimary(t *testing.T) {
	fresh(t)
	s := &stub{name: "a"}
	if p := WithFallback(Candidate{s, "x"}); p != Provider(s) {
		t.Errorf("a chain of one should not be wrapped")
	}
}

func TestFallbackOnUnreachableServer(t *testing.T) {
	fresh(t)
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
	backup := &stub{name: "gemini"}
	evs, err := collect(WithFallback(Candidate{&stub{name: "ollama", err: fmt.Errorf("ollama: %w", refused)}, "qwen3-coder"}, Candidate{backup, "flash"}), "qwen3-coder")
	if err != nil || len(backup.models) != 1 || evs[0].Type != EventNotice || !strings.Contains(evs[0].Text, "(unreachable)") {
		t.Errorf("err = %v, backup calls = %v", err, backup.models)
	}
}

func TestFallbackRestsAFailedCandidate(t *testing.T) {
	fresh(t)
	now := time.Unix(1_000, 0)
	primary := &stub{name: "groq", err: ClassifyStatus(429, errors.New("rate limited"))}
	backup := &stub{name: "ollama"}
	p := WithFallback(Candidate{primary, "a"}, Candidate{backup, "b"}).(*fallback)
	p.now = func() time.Time { return now }

	collect(p, "a")
	evs, _ := collect(p, "a")
	if len(primary.models) != 1 || evs[0].Type == EventNotice {
		t.Errorf("a rate-limited primary should rest: calls %v, first event %+v", primary.models, evs[0])
	}
	now = now.Add(61 * time.Second)
	collect(p, "a")
	if len(primary.models) != 2 {
		t.Errorf("after a minute the primary should be tried again: calls %v", primary.models)
	}
	// A missing model rests for longer.
	primary.err = ClassifyStatus(404, errors.New("model not found"))
	collect(p, "a")
	now = now.Add(5 * time.Minute)
	collect(p, "a")
	if len(primary.models) != 3 {
		t.Errorf("a missing model should rest 10 minutes: calls %v", primary.models)
	}
}

func TestFallbackAlwaysTriesTheLast(t *testing.T) {
	fresh(t)
	quota := ClassifyStatus(402, errors.New("out of credit"))
	a, b := &stub{name: "a", err: quota}, &stub{name: "b", err: quota}
	p := WithFallback(Candidate{a, "x"}, Candidate{b, "y"})
	collect(p, "x")
	if _, err := collect(p, "x"); err == nil || len(b.models) != 2 {
		t.Errorf("the last candidate should be tried every time: err %v, calls %v", err, b.models)
	}
}

// TestNoticeNamesTheCandidateTried: when the next candidate is cooling
// down, the notice names the one actually used.
func TestNoticeNamesTheCandidateTried(t *testing.T) {
	fresh(t)
	down := &APIError{Status: 503, Err: errors.New("overloaded"), Retryable: true}
	a, b, c := &stub{name: "a", err: down}, &stub{name: "b", err: down}, &stub{name: "c"}
	// b fails once on its own and starts cooling down.
	collect(WithFallback(Candidate{b, "y"}, Candidate{c, "z"}), "y")
	events, err := collect(WithFallback(Candidate{a, "x"}, Candidate{b, "y"}, Candidate{c, "z"}), "x")
	if err != nil {
		t.Fatal(err)
	}
	var notice string
	for _, ev := range events {
		if ev.Type == EventNotice {
			notice = ev.Text
		}
	}
	if !strings.Contains(notice, "switched to c/z") {
		t.Fatalf("notice %q should name c/z, the candidate tried", notice)
	}
}
