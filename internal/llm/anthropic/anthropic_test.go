package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/llm/llmtest"
)

var toolSpec = llm.ToolSpec{Name: "read", Description: "Read a file", Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`)}

func collect(t *testing.T, p llm.Provider, req llm.Request) (string, llm.StreamEvent, error) {
	t.Helper()
	var text strings.Builder
	var done llm.StreamEvent
	for ev, err := range p.Stream(context.Background(), req) {
		if err != nil {
			return text.String(), done, err
		}
		switch ev.Type {
		case llm.EventTextDelta:
			text.WriteString(ev.Text)
		case llm.EventDone:
			done = ev
		}
	}
	return text.String(), done, nil
}

func TestStreamToolUse(t *testing.T) {
	body := llmtest.SSE(
		"event: message_start\ndata: "+`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5","content":[],"stop_reason":null,"usage":{"input_tokens":50,"output_tokens":1,"cache_read_input_tokens":20,"cache_creation_input_tokens":0}}}`,
		"event: content_block_start\ndata: "+`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		"event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Need the file."}}`,
		"event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig123"}}`,
		"event: content_block_stop\ndata: "+`{"type":"content_block_stop","index":0}`,
		"event: content_block_start\ndata: "+`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		"event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Let me look."}}`,
		"event: content_block_stop\ndata: "+`{"type":"content_block_stop","index":1}`,
		"event: content_block_start\ndata: "+`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}}`,
		"event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
		"event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"main.go\"}"}}`,
		"event: content_block_stop\ndata: "+`{"type":"content_block_stop","index":2}`,
		"event: message_delta\ndata: "+`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":30}}`,
		"event: message_stop\ndata: "+`{"type":"message_stop"}`,
	)
	srv := llmtest.NewServer(t, 200, body)
	p := New("", "test-key", srv.URL)

	history := []llm.Message{
		llm.UserText("hi"),
		// Thinking from another model must not be replayed.
		{Role: llm.RoleAssistant, Model: "claude-sonnet-5", Blocks: []llm.Block{{Type: llm.BlockThinking, Text: "old", Signature: "oldsig", Provider: Name}, llm.TextBlock("hello")}},
		llm.UserText("read main.go"),
	}
	text, done, err := collect(t, p, llm.Request{Model: "claude-opus-5", System: "sys", Messages: history, Tools: []llm.ToolSpec{toolSpec}, Effort: llm.EffortHigh})
	if err != nil {
		t.Fatal(err)
	}
	if text != "Let me look." {
		t.Errorf("text = %q", text)
	}
	if done.StopReason != llm.StopToolUse {
		t.Errorf("stop = %v", done.StopReason)
	}
	if done.Usage.Input != 50 || done.Usage.CacheRead != 20 || done.Usage.Output != 30 {
		t.Errorf("usage = %+v", done.Usage)
	}
	blocks := done.Message.Blocks
	if len(blocks) != 3 || blocks[0].Signature != "sig123" || blocks[2].Name != "read" || string(blocks[2].Input) != `{"path":"main.go"}` {
		t.Fatalf("blocks = %+v", blocks)
	}

	req := srv.LastBody()
	for _, want := range []string{`"type":"adaptive"`, `"display":"summarized"`, `"effort":"high"`, `"eager_input_streaming":true`, `"additionalProperties":false`, `"cache_control"`, `"stream":true`} {
		if !strings.Contains(req, want) {
			t.Errorf("request missing %s:\n%s", want, req)
		}
	}
	if strings.Contains(req, "oldsig") {
		t.Error("thinking from a different model was replayed")
	}
}

func TestLegacyModelUsesBudget(t *testing.T) {
	srv := llmtest.NewServer(t, 200, llmtest.SSE(
		"event: message_start\ndata: "+`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		"event: message_delta\ndata: "+`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		"event: message_stop\ndata: "+`{"type":"message_stop"}`,
	))
	_, _, err := collect(t, New("", "k", srv.URL), llm.Request{Model: "claude-haiku-4-5", Messages: []llm.Message{llm.UserText("x")}, Effort: llm.EffortMedium})
	if err != nil {
		t.Fatal(err)
	}
	req := srv.LastBody()
	if !strings.Contains(req, `"budget_tokens":8192`) || strings.Contains(req, "effort") || strings.Contains(req, "adaptive") {
		t.Errorf("unexpected thinking config: %s", req)
	}
}

func TestErrors(t *testing.T) {
	srv := llmtest.NewServer(t, 400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 1200000 tokens > 1000000 maximum"}}`)
	_, _, err := collect(t, New("", "k", srv.URL), llm.Request{Model: "claude-opus-5", Messages: []llm.Message{llm.UserText("x")}})
	if !errors.Is(err, llm.ErrContextOverflow) {
		t.Fatalf("want context overflow, got %v", err)
	}
}

func TestThinkingMismatchRetry(t *testing.T) {
	calls := 0
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		calls++
		if calls == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"thinking block prefix_binding mismatch"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, llmtest.SSE(
			"event: message_start\ndata: "+`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-opus-5","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
			"event: message_delta\ndata: "+`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			"event: message_stop\ndata: "+`{"type":"message_stop"}`,
		))
	}))
	defer srv.Close()
	history := []llm.Message{
		llm.UserText("a"),
		{Role: llm.RoleAssistant, Model: "claude-opus-5", Blocks: []llm.Block{{Type: llm.BlockThinking, Text: "t", Signature: "SIGX", Provider: Name}, llm.TextBlock("b")}},
		llm.UserText("c"),
	}
	_, _, err := collect(t, New("", "k", srv.URL), llm.Request{Model: "claude-opus-5", Messages: history})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !strings.Contains(bodies[0], "SIGX") || strings.Contains(bodies[1], "SIGX") {
		t.Fatalf("calls=%d; retry should drop thinking", calls)
	}
}

// TestCutOffToolCallIsInvalid: a tool call whose block never ended (the
// output limit hit mid-call) must not run with whatever input arrived.
func TestCutOffToolCallIsInvalid(t *testing.T) {
	body := llmtest.SSE(
		"event: message_start\ndata: "+`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5","content":[],"stop_reason":null,"usage":{"input_tokens":5,"output_tokens":1}}}`,
		"event: content_block_start\ndata: "+`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"write","input":{}}}`,
		"event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a.go\",\"content\":\"pack"}}`,
		"event: message_delta\ndata: "+`{"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":30}}`,
		"event: message_stop\ndata: "+`{"type":"message_stop"}`,
	)
	srv := llmtest.NewServer(t, 200, body)
	_, done, err := collect(t, New("", "k", srv.URL), llm.Request{Model: "claude-opus-5", Messages: []llm.Message{llm.UserText("x")}})
	if err != nil {
		t.Fatal(err)
	}
	uses := done.Message.ToolUses()
	if len(uses) != 1 || uses[0].Input != nil {
		t.Fatalf("a cut-off call must be marked invalid (nil input), got %+v", uses)
	}
}

func TestToolResultImages(t *testing.T) {
	p, ok := toParam(llm.Block{Type: llm.BlockToolResult, ID: "toolu_1", Content: "Screenshot.",
		Images: []llm.Block{{Type: llm.BlockImage, MediaType: "image/jpeg", Data: "AAAA"}}})
	if !ok {
		t.Fatal("not converted")
	}
	got, _ := json.Marshal(p)
	for _, want := range []string{`"tool_use_id":"toolu_1"`, `{"text":"Screenshot.","type":"text"}`, `"media_type":"image/jpeg"`, `"data":"AAAA"`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("tool_result lacks %s: %s", want, got)
		}
	}
}

// TestPreviousTurnBreakpoint: besides the automatic breakpoint, the end of
// the previous request (the user message before the newest assistant
// message) carries one, so wide turns still hit the cache.
func TestPreviousTurnBreakpoint(t *testing.T) {
	var results []llm.Block
	var uses []llm.Block
	for i := range 15 {
		id := "toolu_" + string(rune('a'+i))
		uses = append(uses, llm.Block{Type: llm.BlockToolUse, ID: id, Name: "read", Input: json.RawMessage(`{}`)})
		results = append(results, llm.Block{Type: llm.BlockToolResult, ID: id, Content: "ok"})
	}
	msgs := []llm.Message{
		llm.UserText("first"),
		{Role: llm.RoleAssistant, Model: "claude-opus-5", Blocks: uses},
		{Role: llm.RoleUser, Blocks: results},
		{Role: llm.RoleAssistant, Model: "claude-opus-5", Blocks: []llm.Block{llm.TextBlock("done?")}},
		llm.UserText("next"),
	}
	params, err := buildParams(llm.Request{Model: "claude-opus-5", Messages: msgs}, Name, false)
	if err != nil {
		t.Fatal(err)
	}
	marked := func(m, b int) bool {
		cc := params.Messages[m].Content[b].GetCacheControl()
		return cc != nil && cc.Type != ""
	}
	if !marked(2, 14) {
		t.Error("the previous request's last block has no breakpoint")
	}
	for m := range params.Messages {
		for b := range params.Messages[m].Content {
			if marked(m, b) && (m != 2 || b != 14) {
				t.Errorf("unexpected breakpoint on message %d block %d", m, b)
			}
		}
	}

	// A first request has no previous turn to mark.
	params, _ = buildParams(llm.Request{Model: "claude-opus-5", Messages: msgs[:1]}, Name, false)
	if cc := params.Messages[0].Content[0].GetCacheControl(); cc.Type != "" {
		t.Error("first request should rely on the automatic breakpoint only")
	}
}
