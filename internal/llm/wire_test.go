package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type wireSink struct {
	mu sync.Mutex
	xs []*WireExchange
}

func (s *wireSink) RecordHTTP(x *WireExchange) {
	s.mu.Lock()
	s.xs = append(s.xs, x)
	s.mu.Unlock()
}

func TestWireTap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"prompt":"hi"}` {
			t.Errorf("the server should get the body unchanged: %q", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Set-Cookie", "session=secret")
		io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		io.WriteString(w, "data: two\n\n")
	}))
	defer srv.Close()

	sink := &wireSink{}
	send := func(ctx context.Context) string {
		req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"/v1/chat?key=sk-secret&alt=sse", strings.NewReader(`{"prompt":"hi"}`))
		req.Header.Set("Authorization", "Bearer sk-secret")
		req.Header.Set("X-Api-Key", "sk-secret")
		resp, err := HTTPClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return string(out)
	}

	if got := send(context.Background()); got != "data: one\n\ndata: two\n\n" || len(sink.xs) != 0 {
		t.Fatalf("an untagged request passes straight through: %q, %d recorded", got, len(sink.xs))
	}
	if got := send(WithWire(context.Background(), sink, "r7")); got != "data: one\n\ndata: two\n\n" {
		t.Fatalf("the caller should read the whole stream: %q", got)
	}
	if len(sink.xs) != 1 {
		t.Fatalf("%d exchanges recorded", len(sink.xs))
	}
	x := sink.xs[0]
	if x.Req != "r7" || x.Status != 200 || string(x.ReqBody) != `{"prompt":"hi"}` || string(x.RespBody) != "data: one\n\ndata: two\n\n" {
		t.Errorf("exchange: %+v", x)
	}
	all := x.URL + x.ReqHeader.Get("Authorization") + x.ReqHeader.Get("X-Api-Key") + x.RespHeader.Get("Set-Cookie")
	if strings.Contains(all, "sk-secret") || strings.Contains(all, "session=secret") || !strings.Contains(x.URL, "alt=sse") {
		t.Errorf("credentials must be redacted: %s %v %v", x.URL, x.ReqHeader, x.RespHeader)
	}
	if x.Sent.IsZero() || x.Done.Before(x.Headers) || x.Headers.Before(x.Sent) {
		t.Errorf("timing: %v %v %v", x.Sent, x.Headers, x.Done)
	}
}
