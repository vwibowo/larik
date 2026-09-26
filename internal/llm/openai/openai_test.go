package openai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/llm/llmtest"
)

func TestResponsesStream(t *testing.T) {
	reasoning := `{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"Plan: read."}],"encrypted_content":"ENC"}`
	call := `{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read","arguments":"{\"path\":\"a.go\"}","status":"completed"}`
	msg := `{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Reading.","annotations":[]}]}`
	body := llmtest.SSE(
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"Plan: read.","sequence_number":1}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"Reading.","sequence_number":2}`,
		`{"type":"response.output_item.added","output_index":2,"item":`+call+`,"sequence_number":3}`,
		`{"type":"response.completed","sequence_number":4,"response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.5","output":[`+reasoning+`,`+msg+`,`+call+`],"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":40},"output_tokens":20,"output_tokens_details":{"reasoning_tokens":5},"total_tokens":120}}}`,
	)
	srv := llmtest.NewServer(t, 200, body)
	p := New("k", srv.URL)

	prior := llm.Message{Role: llm.RoleAssistant, Model: "gpt-5.5", Blocks: []llm.Block{
		{Type: llm.BlockOpaque, Provider: Name, Raw: json.RawMessage(`{"type":"reasoning","id":"rs_0","encrypted_content":"PRIOR"}`)},
		{Type: llm.BlockToolUse, ID: "call_0", Name: "read", Input: json.RawMessage(`{"path":"x"}`)},
	}}
	req := llm.Request{
		Model:  "gpt-5.5",
		System: "sys",
		Messages: []llm.Message{
			llm.UserText("go"),
			prior,
			{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ID: "call_0", Name: "read", Content: "data"}}},
		},
		Tools:  []llm.ToolSpec{{Name: "read", Description: "d", Schema: json.RawMessage(`{"type":"object","properties":{}}`)}},
		Effort: llm.EffortXHigh,
	}
	var done llm.StreamEvent
	var thinking string
	for ev, err := range p.Stream(context.Background(), req) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Type == llm.EventThinkingDelta {
			thinking += ev.Text
		}
		if ev.Type == llm.EventDone {
			done = ev
		}
	}
	if thinking != "Plan: read." {
		t.Errorf("thinking = %q", thinking)
	}
	if done.StopReason != llm.StopToolUse || done.Usage.Input != 60 || done.Usage.CacheRead != 40 {
		t.Errorf("done = %+v", done)
	}
	uses := done.Message.ToolUses()
	if len(uses) != 1 || uses[0].ID != "call_1" || string(uses[0].Input) != `{"path":"a.go"}` {
		t.Fatalf("tool uses = %+v", uses)
	}
	if done.Message.Text() != "Reading." {
		t.Errorf("text = %q", done.Message.Text())
	}

	body2 := srv.LastBody()
	for _, want := range []string{`"store":false`, `"reasoning.encrypted_content"`, `"effort":"xhigh"`, `"instructions":"sys"`, `"encrypted_content":"PRIOR"`, `"type":"function_call_output"`, `"call_id":"call_0"`, `"type":"function"`} {
		if !strings.Contains(body2, want) {
			t.Errorf("request missing %s:\n%s", want, body2)
		}
	}
}

func TestReasoningModel(t *testing.T) {
	for m, want := range map[string]bool{"gpt-5.5": true, "gpt-6-sol": true, "o3": true, "gpt-4.1": false, "gpt-5.1-chat-latest": false} {
		if got := reasoningModel(m); got != want {
			t.Errorf("%s: got %v", m, got)
		}
	}
}
