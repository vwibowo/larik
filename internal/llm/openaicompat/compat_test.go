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

// TestWholeCallsAtOneIndex covers servers that send each tool call whole,
// all at index 0: each new id is a call of its own.
func TestWholeCallsAtOneIndex(t *testing.T) {
	body := llmtest.SSE(
		`{"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"read","arguments":"{\"path\":\"a\"}"}}]}}]}`,
		`{"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"b","type":"function","function":{"name":"read","arguments":"{\"path\":\"b\"}"}}]}}]}`,
		`{"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	)
	srv := llmtest.NewServer(t, 200, body)
	var done llm.StreamEvent
	for ev, err := range New("ollama", "", srv.URL).Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("hi")}}) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Type == llm.EventDone {
			done = ev
		}
	}
	uses := done.Message.ToolUses()
	if len(uses) != 2 || uses[0].ID != "a" || string(uses[0].Input) != `{"path":"a"}` || uses[1].ID != "b" || string(uses[1].Input) != `{"path":"b"}` {
		t.Fatalf("tool uses = %+v", uses)
	}
}

func TestToolResultImagesFollowAsUserMessage(t *testing.T) {
	p := New("x", "key", "http://x")
	msgs := p.messages(llm.Request{Messages: []llm.Message{
		{Role: llm.RoleUser, Blocks: []llm.Block{
			{Type: llm.BlockToolResult, ID: "call_1", Name: "browser_screenshot", Content: "Screenshot.", Images: []llm.Block{{Type: llm.BlockImage, MediaType: "image/png", Data: "AAAA"}}},
			{Type: llm.BlockToolResult, ID: "call_2", Name: "read", Content: "text"},
		}},
	}})
	if len(msgs) != 3 || msgs[0]["role"] != "tool" || msgs[1]["role"] != "tool" || msgs[2]["role"] != "user" {
		t.Fatalf("want two tool messages, then the images: %v", msgs)
	}
	got, _ := json.Marshal(msgs[2]["content"])
	for _, want := range []string{`call=\"call_1\"`, `"url":"data:image/png;base64,AAAA"`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("user message lacks %s: %s", want, got)
		}
	}
}

// drain runs a request against srv and discards the events, so a test can
// assert on the body that was sent.
func drainTo(t *testing.T, p *Provider, req llm.Request) {
	t.Helper()
	for _, err := range p.Stream(context.Background(), req) {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func okBody() string {
	return llmtest.SSE(
		`{"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}`,
		`[DONE]`,
	)
}

// top_k is not in the OpenAI schema, so it rides along as an extra field;
// temperature and top_p are ordinary parameters.
func TestSamplingReachesTheRequestBody(t *testing.T) {
	srv := llmtest.NewServer(t, 200, okBody())
	p := New("ollama", "", srv.URL)
	temp, topP, topK := 0.7, 0.8, 20
	drainTo(t, p, llm.Request{
		Model: "m", Messages: []llm.Message{llm.UserText("hi")},
		Sampling: &llm.Sampling{Temperature: &temp, TopP: &topP, TopK: &topK},
	})
	sent := srv.LastBody()
	for _, want := range []string{`"temperature":0.7`, `"top_p":0.8`, `"top_k":20`} {
		if !strings.Contains(sent, want) {
			t.Errorf("missing %s in %s", want, sent)
		}
	}
}

func TestNoSamplingSendsNoDecodingParams(t *testing.T) {
	srv := llmtest.NewServer(t, 200, okBody())
	p := New("ollama", "", srv.URL)
	drainTo(t, p, llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("hi")}})
	sent := srv.LastBody()
	for _, key := range []string{"temperature", "top_p", "top_k"} {
		if strings.Contains(sent, `"`+key+`"`) {
			t.Errorf("unconfigured request sent %s: %s", key, sent)
		}
	}
}
