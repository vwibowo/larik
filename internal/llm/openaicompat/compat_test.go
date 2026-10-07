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

// Switching to a local model mid-session leaves document blocks in the
// transcript. This API has nowhere to put them, so they are dropped rather
// than sent as something the server would reject; the marker text that
// Larik attached alongside survives, so the model still knows a document
// was there.
func TestDocumentBlocksAreDroppedNotSent(t *testing.T) {
	srv := llmtest.NewServer(t, 200, okBody())
	p := New("ollama", "", srv.URL)
	drainTo(t, p, llm.Request{
		Model: "m",
		Messages: []llm.Message{{Role: llm.RoleUser, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: `<document path="spec.pdf" pages="3"/>`, Attachment: "spec.pdf"},
			{Type: llm.BlockDocument, MediaType: "application/pdf", Data: "JVBERi0=", Pages: 3, Attachment: "spec.pdf"},
		}}},
	})
	sent := srv.LastBody()
	if strings.Contains(sent, "JVBERi0=") || strings.Contains(sent, "application/pdf") {
		t.Errorf("the document payload should not be sent: %s", sent)
	}
	if !strings.Contains(sent, "spec.pdf") {
		t.Errorf("the marker text should survive: %s", sent)
	}
	// And the provider does not claim to take documents.
	if llm.AcceptsDocuments(p, "application/pdf") {
		t.Error("this API has no document block")
	}
}

// collectAll runs a request and returns every event, so a test can assert
// on the notice as well as the message.
func collectAll(t *testing.T, p *Provider, req llm.Request) []llm.StreamEvent {
	t.Helper()
	var evs []llm.StreamEvent
	for ev, err := range p.Stream(context.Background(), req) {
		if err != nil {
			t.Fatal(err)
		}
		evs = append(evs, ev)
	}
	return evs
}

func salvageReq() llm.Request {
	return llm.Request{
		Model:    "m",
		Messages: []llm.Message{llm.UserText("read a.go")},
		Tools:    []llm.ToolSpec{{Name: "read", Description: "d", Schema: json.RawMessage(`{"type":"object"}`)}},
	}
}

// A server that leaves the tool call in the text must not end the turn on
// markup: the call is recovered and the turn continues.
func TestToolCallWrittenAsTextIsRecovered(t *testing.T) {
	srv := llmtest.NewServer(t, 200, llmtest.SSE(
		`{"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"I'll read it.\n<tool_call>{\"name\": \"read\", \"arguments\": {\"path\": \"a.go\"}}</tool_call>"},"finish_reason":"stop"}]}`,
		`[DONE]`,
	))
	p := New("lmstudio", "", srv.URL)
	evs := collectAll(t, p, salvageReq())

	var done llm.StreamEvent
	var notice, started string
	for _, ev := range evs {
		switch ev.Type {
		case llm.EventDone:
			done = ev
		case llm.EventNotice:
			notice = ev.Text
		case llm.EventToolUseStart:
			started = ev.Text
		}
	}
	uses := done.Message.ToolUses()
	if len(uses) != 1 || uses[0].Name != "read" || string(uses[0].Input) != `{"path": "a.go"}` {
		t.Fatalf("tool uses = %+v", uses)
	}
	// The turn has to report tool use, or the agent loop would stop here.
	if done.StopReason != llm.StopToolUse {
		t.Errorf("stop reason = %q, want tool_use", done.StopReason)
	}
	// The markup is gone from what the user and transcript see; the prose
	// the model actually wrote stays.
	if got := done.Message.Text(); got != "I'll read it." {
		t.Errorf("text = %q", got)
	}
	// A salvage is never silent, and the tool shows as starting.
	if !strings.Contains(notice, "recovered a tool call") {
		t.Errorf("notice = %q", notice)
	}
	if started != "read" {
		t.Errorf("tool-use-start = %q", started)
	}
}

// A structured call is authoritative: text that merely looks like a call
// must not be salvaged on top of it, which would double-run the tool.
func TestAStructuredCallSuppressesSalvage(t *testing.T) {
	srv := llmtest.NewServer(t, 200, llmtest.SSE(
		`{"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"example: <tool_call>{\"name\":\"read\",\"arguments\":{\"path\":\"wrong.go\"}}</tool_call>","tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read","arguments":"{\"path\":\"right.go\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	))
	p := New("lmstudio", "", srv.URL)
	evs := collectAll(t, p, salvageReq())

	var done llm.StreamEvent
	for _, ev := range evs {
		if ev.Type == llm.EventDone {
			done = ev
		}
		if ev.Type == llm.EventNotice {
			t.Errorf("no salvage notice expected, got %q", ev.Text)
		}
	}
	uses := done.Message.ToolUses()
	if len(uses) != 1 || string(uses[0].Input) != `{"path":"right.go"}` {
		t.Fatalf("the structured call must win: %+v", uses)
	}
	// The text, markup and all, is left exactly as the model wrote it.
	if !strings.Contains(done.Message.Text(), "wrong.go") {
		t.Errorf("text should be untouched, got %q", done.Message.Text())
	}
}

// Prose that names no offered tool is left alone and still ends the turn.
func TestProseThatIsNotACallEndsTheTurnNormally(t *testing.T) {
	srv := llmtest.NewServer(t, 200, llmtest.SSE(
		`{"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"The file looks fine to me."},"finish_reason":"stop"}]}`,
		`[DONE]`,
	))
	p := New("lmstudio", "", srv.URL)
	for _, ev := range collectAll(t, p, salvageReq()) {
		if ev.Type == llm.EventNotice {
			t.Errorf("unexpected notice %q", ev.Text)
		}
		if ev.Type == llm.EventDone {
			if ev.StopReason != llm.StopEnd {
				t.Errorf("stop reason = %q, want end_turn", ev.StopReason)
			}
			if ev.Message.Text() != "The file looks fine to me." {
				t.Errorf("text = %q", ev.Message.Text())
			}
		}
	}
}
