package llm

import (
	"bufio"
	"context"
	"errors"
	"iter"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// lineProvider streams a URL with the shared client, one text delta per
// line, the way the adapters' SDKs do. Like them, it tries again when the
// connection fails.
type lineProvider struct {
	url     string
	retries int
}

func (p lineProvider) Name() string { return "lines" }

func (p lineProvider) Stream(ctx context.Context, req Request) iter.Seq2[StreamEvent, error] {
	return func(yield func(StreamEvent, error) bool) {
		var resp *http.Response
		var err error
		for attempt := 0; attempt <= p.retries; attempt++ {
			hreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
			if resp, err = HTTPClient.Do(hreq); err == nil {
				break
			}
		}
		if err != nil {
			yield(StreamEvent{}, err)
			return
		}
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if !yield(StreamEvent{Type: EventTextDelta, Text: sc.Text()}, nil) {
				return
			}
		}
		if err := sc.Err(); err != nil {
			yield(StreamEvent{}, err)
			return
		}
		yield(StreamEvent{Type: EventDone, Message: Message{Role: RoleAssistant, Blocks: []Block{TextBlock("done")}}}, nil)
	}
}

// runStream runs a stream to its end.
func runStream(p Provider) (texts []string, done bool, err error) {
	for ev, e := range p.Stream(context.Background(), Request{Model: "m"}) {
		switch {
		case e != nil:
			err = e
		case ev.Type == EventDone:
			done = true
		default:
			texts = append(texts, ev.Text)
		}
	}
	return texts, done, err
}

func TestStallTimeout(t *testing.T) {
	const d = 150 * time.Millisecond
	var hits atomic.Int32
	mux := http.NewServeMux()
	// Accepts the request and never answers.
	mux.HandleFunc("/silent", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-r.Context().Done()
	})
	// Starts answering, then goes quiet.
	mux.HandleFunc("/quiet", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("one\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	// Slow overall, but never quiet for as long as the timeout.
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		for _, line := range []string{"one", "two", "three", "four", "five", "six"} {
			time.Sleep(d / 3)
			w.Write([]byte(line + "\n"))
			w.(http.Flusher).Flush()
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Run("no response", func(t *testing.T) {
		hits.Store(0)
		start := time.Now()
		_, done, err := runStream(WithStallTimeout(lineProvider{url: srv.URL + "/silent", retries: 2}, d))
		var stall *StallError
		if done || !errors.As(err, &stall) || stall.Provider != "lines" || stall.After != d {
			t.Fatalf("done = %v, err = %v; want a StallError", done, err)
		}
		if took := time.Since(start); took > 5*d {
			t.Fatalf("took %s; the client's retries each waited the timeout again", took)
		}
		if n := hits.Load(); n != 1 {
			t.Fatalf("server saw %d requests, want 1: a stalled request must not be retried", n)
		}
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() || !ShouldFallBack(err) {
			t.Fatalf("a stall should count as a timeout worth falling back for: %v", err)
		}
	})

	t.Run("goes quiet partway", func(t *testing.T) {
		texts, done, err := runStream(WithStallTimeout(lineProvider{url: srv.URL + "/quiet"}, d))
		var stall *StallError
		if done || !errors.As(err, &stall) || strings.Join(texts, ",") != "one" {
			t.Fatalf("texts = %v, done = %v, err = %v", texts, done, err)
		}
	})

	t.Run("slow but alive", func(t *testing.T) {
		texts, done, err := runStream(WithStallTimeout(lineProvider{url: srv.URL + "/slow"}, d))
		if err != nil || !done || len(texts) != 6 {
			t.Fatalf("texts = %v, done = %v, err = %v", texts, done, err)
		}
	})

	t.Run("the user's interrupt is not a stall", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(d/3, cancel)
		var err error
		for _, e := range WithStallTimeout(lineProvider{url: srv.URL + "/silent"}, d).Stream(ctx, Request{}) {
			err = e
		}
		var stall *StallError
		if !errors.Is(err, context.Canceled) || errors.As(err, &stall) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})

	t.Run("off", func(t *testing.T) {
		p := lineProvider{url: srv.URL + "/slow"}
		if got := WithStallTimeout(p, 0); got != Provider(p) {
			t.Fatalf("a zero timeout should leave the provider alone, got %T", got)
		}
	})

	t.Run("falls back", func(t *testing.T) {
		chain := WithFallback(
			Candidate{WithStallTimeout(lineProvider{url: srv.URL + "/silent"}, d), "stuck"},
			Candidate{WithStallTimeout(lineProvider{url: srv.URL + "/slow"}, d), "fine"},
		)
		var notice string
		done := false
		for ev, err := range chain.Stream(context.Background(), Request{Model: "stuck"}) {
			if err != nil {
				t.Fatal(err)
			}
			switch ev.Type {
			case EventNotice:
				notice = ev.Text
			case EventDone:
				done = true
			}
		}
		if !done || !strings.Contains(notice, "timed out") || !strings.Contains(notice, "lines/fine") {
			t.Fatalf("done = %v, notice = %q", done, notice)
		}
	})
}
