package anthropic

import (
	"context"
	"encoding/json"
	"errors"
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
	p := New("test-key", srv.URL)

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
	_, _, err := collect(t, New("k", srv.URL), llm.Request{Model: "claude-haiku-4-5", Messages: []llm.Message{llm.UserText("x")}, Effort: llm.EffortMedium})
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
	_, _, err := collect(t, New("k", srv.URL), llm.Request{Model: "claude-opus-5", Messages: []llm.Message{llm.UserText("x")}})
	if !errors.Is(err, llm.ErrContextOverflow) {
		t.Fatalf("want context overflow, got %v", err)
	}
}
