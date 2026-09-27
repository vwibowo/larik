package openaicompat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/llm/llmtest"
)

func TestChatStream(t *testing.T) {
	body := llmtest.SSE(
		`{"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"think "}}]}`,
		`{"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"Sure."}}]}`,
		`{"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"bash","arguments":"{\"comm"}}]}}]}`,
		`{"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"and\":\"ls\"}"}}]}}]}`,
		`{"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"id":"c1","object":"chat.completion.chunk","model":"m","choices":[],"usage":{"prompt_tokens":30,"completion_tokens":7,"total_tokens":37}}`,
		`[DONE]`,
	)
	srv := llmtest.NewServer(t, 200, body)
	p := New("ollama", "", srv.URL)

	req := llm.Request{
		Model:  "m",
		System: "sys",
		Messages: []llm.Message{
			llm.UserText("hi"),
			{Role: llm.RoleAssistant, Model: "m", Blocks: []llm.Block{{Type: llm.BlockThinking, Text: "prior", Provider: "ollama"}, {Type: llm.BlockToolUse, ID: "c0", Name: "bash", Input: json.RawMessage(`{"command":"pwd"}`)}}},
			{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ID: "c0", Content: "/x"}}},
		},
		Tools: []llm.ToolSpec{{Name: "bash", Description: "d", Schema: json.RawMessage(`{"type":"object"}`)}},
	}
	var done llm.StreamEvent
	for ev, err := range p.Stream(context.Background(), req) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Type == llm.EventDone {
			done = ev
		}
	}
	uses := done.Message.ToolUses()
	if len(uses) != 1 || uses[0].ID != "call_a" || string(uses[0].Input) != `{"command":"ls"}` {
		t.Fatalf("tool uses = %+v", uses)
	}
	if done.Message.Blocks[0].Text != "think " || done.Message.Text() != "Sure." {
		t.Errorf("blocks = %+v", done.Message.Blocks)
	}
	if done.StopReason != llm.StopToolUse || done.Usage.Input != 30 || done.Usage.Output != 7 {
		t.Errorf("done = %+v", done)
	}
	sent := srv.LastBody()
	for _, want := range []string{`"role":"system"`, `"role":"tool"`, `"tool_call_id":"c0"`, `"reasoning_content":"prior"`, `"include_usage":true`, `"tool_calls"`} {
		if !strings.Contains(sent, want) {
			t.Errorf("request missing %s:\n%s", want, sent)
		}
	}
}

func TestOllamaProbe(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ps", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"models":[{"name":"qwen3:4b","model":"qwen3:4b","context_length":4096}]}`)
	})
	mux.HandleFunc("/api/show", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Model string }
		json.NewDecoder(r.Body).Decode(&in)
		if in.Model == "embed" {
			io.WriteString(w, `{"capabilities":["embedding"]}`)
			return
		}
		io.WriteString(w, `{"capabilities":["completion","tools"]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	p := New("ollama", "", srv.URL+"/v1")
	ctx := context.Background()

	if w := p.ContextWindow(ctx, "qwen3:4b"); w != 4096 {
		t.Errorf("window = %d", w)
	}
	if w := p.ContextWindow(ctx, "not-loaded"); w != 0 {
		t.Errorf("unloaded model should report 0, got %d", w)
	}
	if ok, known := p.SupportsTools(ctx, "qwen3:4b"); !ok || !known {
		t.Error("qwen3 supports tools")
	}
	if ok, known := p.SupportsTools(ctx, "embed"); ok || !known {
		t.Error("embedding model does not support tools")
	}
	other := New("groq", "k", srv.URL+"/v1")
	if other.ContextWindow(ctx, "qwen3:4b") != 0 {
		t.Error("non-Ollama endpoints must not be probed")
	}
}
