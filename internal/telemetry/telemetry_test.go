package telemetry

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"larik/internal/agent"
	"larik/internal/llm"
)

// collector is a fake OTLP/HTTP endpoint.
type collector struct {
	*httptest.Server
	mu      sync.Mutex
	bodies  []string
	headers []http.Header
	status  int
}

func newCollector(t *testing.T) *collector {
	t.Helper()
	c := &collector{status: 200}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		defer c.mu.Unlock()
		if r.URL.Path != "/v1/traces" || r.Method != http.MethodPost {
			w.WriteHeader(404)
			return
		}
		c.bodies = append(c.bodies, string(b))
		c.headers = append(c.headers, r.Header.Clone())
		w.WriteHeader(c.status)
	}))
	t.Cleanup(c.Close)
	return c
}

// decoded span, as a collector would read it.
type got struct {
	TraceID, SpanID, ParentSpanID, Name string
	Status                              struct{ Code int }
	Attributes                          []struct {
		Key   string
		Value map[string]any
	}
}

func (g got) attr(k string) any {
	for _, a := range g.Attributes {
		if a.Key == k {
			for _, v := range a.Value {
				return v
			}
		}
	}
	return nil
}

func (c *collector) spans(t *testing.T) []got {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []got
	for _, body := range c.bodies {
		var p struct {
			ResourceSpans []struct {
				Resource   struct{ Attributes []struct{ Key string } }
				ScopeSpans []struct{ Spans []got }
			}
		}
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			t.Fatalf("collector got invalid JSON: %v\n%s", err, body)
		}
		for _, rs := range p.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				out = append(out, ss.Spans...)
			}
		}
	}
	return out
}

func find(spans []got, name string) *got {
	for i := range spans {
		if strings.HasPrefix(spans[i].Name, name) {
			return &spans[i]
		}
	}
	return nil
}

func start(t *testing.T, c *collector) *Exporter {
	t.Helper()
	retryDelay = time.Millisecond
	x, err := New(Config{Endpoint: c.URL, Headers: map[string]string{"Authorization": "Bearer t0ken"}, Version: "9.9", LogPath: filepath.Join(t.TempDir(), "telemetry.log")})
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func TestEndpointValidation(t *testing.T) {
	for in, want := range map[string]string{
		"http://localhost:4318":              "http://localhost:4318/v1/traces",
		"http://localhost:4318/":             "http://localhost:4318/v1/traces",
		"https://otel.example/v1/traces":     "https://otel.example/v1/traces",
		"https://otel.example/base?x=1#frag": "https://otel.example/base/v1/traces",
	} {
		if got, err := tracesURL(in); err != nil || got != want {
			t.Errorf("tracesURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "localhost:4318", "ftp://x", "http://", "not a url"} {
		if _, err := tracesURL(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if h := ParseHeaders("a=1, b = two ,junk,=x"); len(h) != 2 || h["a"] != "1" || h["b"] != "two" {
		t.Errorf("headers = %v", h)
	}
}

// session plays one turn: a model request, a tool call and a finished turn.
func session(o agent.Observer) {
	root := agent.Origin{}
	o.Observe(root, agent.Event{Kind: agent.EvToolStart, ToolID: "t1", ToolName: "bash", Input: json.RawMessage(`{"command":"cat /home/me/SECRET-PATH"}`)})
	o.Observe(root, agent.Event{Kind: agent.EvToolEnd, ToolID: "t1", ToolName: "bash", Output: "SECRET-OUTPUT", IsError: true})
	o.Observe(root, agent.Event{Kind: agent.EvUsage, Usage: &agent.UsageInfo{
		Model: "claude-x", Turn: llm.Usage{Input: 100, Output: 20, CacheRead: 400, CacheWrite: 50}, CostUSD: 0.0123,
		ContextTokens: 550, ContextWindow: 200000, RequestMS: 1500, TTFTMS: 300}})
	o.Observe(root, agent.Event{Kind: agent.EvAssistant, Text: "SECRET-REPLY"})
	o.Observe(root, agent.Event{Kind: agent.EvDone, StopReason: "end_turn", Text: "SECRET-DONE"})
}

func TestSessionBecomesTraceAndLeaksNoContent(t *testing.T) {
	c := newCollector(t)
	x := start(t, c)
	session(x.Observer("sess-1"))
	x.Close()

	spans := c.spans(t)
	turn, tool, chat := find(spans, "larik.turn"), find(spans, "execute_tool bash"), find(spans, "chat claude-x")
	if turn == nil || tool == nil || chat == nil || len(spans) != 3 {
		t.Fatalf("want a turn, a tool and a chat span, got %d: %+v", len(spans), spans)
	}
	if tool.TraceID != turn.TraceID || chat.TraceID != turn.TraceID || tool.ParentSpanID != turn.SpanID || chat.ParentSpanID != turn.SpanID || turn.ParentSpanID != "" {
		t.Errorf("tool and chat should hang off the turn in one trace: %+v", spans)
	}
	if len(turn.TraceID) != 32 || len(turn.SpanID) != 16 {
		t.Errorf("ids must be 32 and 16 hex characters: %q %q", turn.TraceID, turn.SpanID)
	}
	if tool.Status.Code != 2 || turn.Status.Code != 0 {
		t.Errorf("a failed tool is an error span; an ended turn is not: tool=%d turn=%d", tool.Status.Code, turn.Status.Code)
	}
	for k, want := range map[string]any{
		"gen_ai.usage.input_tokens": "550", "gen_ai.usage.output_tokens": "20", "gen_ai.request.model": "claude-x",
		"larik.cost_usd": 0.0123, "larik.ttft_ms": "300", "larik.session.id": "sess-1",
	} {
		if v := chat.attr(k); v != want {
			t.Errorf("chat %s = %v (%T), want %v", k, v, v, want)
		}
	}
	if turn.attr("larik.stop_reason") != "end_turn" || turn.attr("larik.requests") != "1" || turn.attr("gen_ai.usage.input_tokens") != "550" {
		t.Errorf("turn totals: %+v", turn.Attributes)
	}
	// Nothing the model or the tools said, or any path, may reach the collector.
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, body := range c.bodies {
		for _, secret := range []string{"SECRET-PATH", "SECRET-OUTPUT", "SECRET-REPLY", "SECRET-DONE", "/home/me"} {
			if strings.Contains(body, secret) {
				t.Errorf("exported payload contains %q", secret)
			}
		}
	}
	if c.headers[0].Get("Authorization") != "Bearer t0ken" || c.headers[0].Get("Content-Type") != "application/json" {
		t.Errorf("request headers: %v", c.headers[0])
	}
	if !strings.Contains(c.bodies[0], `"service.name"`) || !strings.Contains(c.bodies[0], `"9.9"`) {
		t.Error("the resource should name the service and its version")
	}
}

func TestSubagentSpansNestUnderTheirTaskCall(t *testing.T) {
	c := newCollector(t)
	x := start(t, c)
	o := x.Observer("s")
	root := agent.Origin{}
	child := agent.Origin{Agent: "explore: find auth", Parent: "task-1"}
	o.Observe(root, agent.Event{Kind: agent.EvToolStart, ToolID: "task-1", ToolName: "task"})
	o.Observe(child, agent.Event{Kind: agent.EvToolStart, ToolID: "g1", ToolName: "grep"})
	o.Observe(child, agent.Event{Kind: agent.EvToolEnd, ToolID: "g1", ToolName: "grep"})
	o.Observe(child, agent.Event{Kind: agent.EvDone, StopReason: "loop"})
	o.Observe(root, agent.Event{Kind: agent.EvToolEnd, ToolID: "task-1", ToolName: "task"})
	o.Observe(root, agent.Event{Kind: agent.EvDone, StopReason: "end_turn"})
	x.Close()

	spans := c.spans(t)
	turn, task, sub, grep := find(spans, "larik.turn"), find(spans, "execute_tool task"), find(spans, "larik.subagent"), find(spans, "execute_tool grep")
	if turn == nil || task == nil || sub == nil || grep == nil {
		t.Fatalf("missing spans: %+v", spans)
	}
	if sub.ParentSpanID != task.SpanID || grep.ParentSpanID != sub.SpanID || task.ParentSpanID != turn.SpanID {
		t.Errorf("want turn > task > subagent > grep, got %+v", spans)
	}
	if sub.TraceID != turn.TraceID || grep.TraceID != turn.TraceID {
		t.Error("a subagent belongs to its turn's trace")
	}
	if sub.attr("larik.agent") != "explore: find auth" || sub.Status.Code != 2 {
		t.Errorf("subagent: agent=%v status=%d (a loop is a failure)", sub.attr("larik.agent"), sub.Status.Code)
	}
}

func TestBackgroundSubagentStillNestsAfterItsCallEnded(t *testing.T) {
	c := newCollector(t)
	x := start(t, c)
	o := x.Observer("s")
	root, child := agent.Origin{}, agent.Origin{Agent: "worker", Parent: "task-9"}
	o.Observe(root, agent.Event{Kind: agent.EvToolStart, ToolID: "task-9", ToolName: "task"})
	o.Observe(root, agent.Event{Kind: agent.EvToolEnd, ToolID: "task-9", ToolName: "task"})
	o.Observe(root, agent.Event{Kind: agent.EvDone, StopReason: "end_turn"})
	// Later, with no turn running, the background child finishes.
	o.Observe(child, agent.Event{Kind: agent.EvUsage, Usage: &agent.UsageInfo{Model: "m", Turn: llm.Usage{Input: 1}}})
	o.Observe(child, agent.Event{Kind: agent.EvDone, StopReason: "end_turn"})
	x.Close()

	spans := c.spans(t)
	task, sub := find(spans, "execute_tool task"), find(spans, "larik.subagent")
	if task == nil || sub == nil || sub.ParentSpanID != task.SpanID || sub.TraceID != task.TraceID {
		t.Errorf("the child should nest under the finished task call: %+v", spans)
	}
}

func TestInterruptedTurnClosesOpenToolSpans(t *testing.T) {
	c := newCollector(t)
	x := start(t, c)
	o := x.Observer("s")
	o.Observe(agent.Origin{}, agent.Event{Kind: agent.EvToolStart, ToolID: "t", ToolName: "bash"})
	o.Observe(agent.Origin{}, agent.Event{Kind: agent.EvDone, StopReason: "interrupted"})
	x.Close()
	spans := c.spans(t)
	if tool := find(spans, "execute_tool bash"); tool == nil || tool.Status.Code != 2 || find(spans, "larik.turn") == nil {
		t.Errorf("a tool still running when the turn ends is closed as an error: %+v", spans)
	}
}

func TestFailedDeliveryNeverBlocksAndIsLogged(t *testing.T) {
	c := newCollector(t)
	c.status = 503
	logPath := filepath.Join(t.TempDir(), "telemetry.log")
	retryDelay = time.Millisecond
	x, err := New(Config{Endpoint: c.URL + "/", LogPath: logPath})
	if err != nil {
		t.Fatal(err)
	}
	// A delivery while running is tried twice before it is given up.
	x.send([]span{{trace: randomHex(16), id: randomHex(8), name: "s", start: time.Now(), end: time.Now()}})
	if x.Failed() != 1 {
		t.Errorf("failed batches = %d, want 1", x.Failed())
	}
	c.mu.Lock()
	tries := len(c.bodies)
	c.mu.Unlock()
	if tries != 2 {
		t.Errorf("one try and one retry, got %d", tries)
	}
	// What is still queued at exit is tried once, so quitting stays quick.
	began := time.Now()
	session(x.Observer("s"))
	x.Close()
	if time.Since(began) > 3*time.Second {
		t.Error("an unavailable collector must not hold the agent up")
	}
	c.mu.Lock()
	if len(c.bodies) != 3 {
		t.Errorf("the final flush is not retried, got %d requests in all", len(c.bodies))
	}
	c.mu.Unlock()
	if log, _ := os.ReadFile(logPath); !strings.Contains(string(log), "503") || strings.Count(string(log), "\n") != 2 {
		t.Errorf("each failed delivery should be logged: %q", log)
	}
}

func TestFullQueueDropsInsteadOfBlocking(t *testing.T) {
	x := &Exporter{in: make(chan span, 2)}
	for range 5 {
		x.emit(span{name: "s"})
	}
	if x.Dropped() != 3 {
		t.Errorf("dropped = %d, want 3", x.Dropped())
	}
	var nilExporter *Exporter
	nilExporter.emit(span{})
	nilExporter.Close()
	if nilExporter.Observer("s") != nil {
		t.Error("no exporter means no observer")
	}
}

func TestUnreachableCollectorDoesNotPanic(t *testing.T) {
	retryDelay = time.Millisecond
	x, err := New(Config{Endpoint: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	session(x.Observer("s"))
	x.Close()
	if x.Failed() == 0 {
		t.Error("an unreachable collector counts as a failure")
	}
}
