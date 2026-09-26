// Package llmtest provides a fake SSE server for provider adapter tests.
package llmtest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Server replays a canned SSE body and captures the request.
type Server struct {
	*httptest.Server
	mu   sync.Mutex
	Body string // last request body
	Path string // last request path
}

// SSE builds an event stream. Each item is either "event: x\ndata: {...}"
// or a bare JSON payload that becomes "data: {...}".
func SSE(items ...string) string {
	var b strings.Builder
	for _, it := range items {
		if !strings.HasPrefix(it, "event:") && !strings.HasPrefix(it, "data:") {
			it = "data: " + it
		}
		b.WriteString(it + "\n\n")
	}
	return b.String()
}

func NewServer(t *testing.T, status int, body string) *Server {
	s := &Server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.Body, s.Path = string(data), r.URL.Path
		s.mu.Unlock()
		if status != 200 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			io.WriteString(w, body)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) LastBody() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Body
}
