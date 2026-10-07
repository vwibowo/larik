package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"larik/internal/llm"
)

// fakeServer serves /api/chat from a canned NDJSON stream and records the
// last chat request; /api/show reports maxCtx.
type fakeServer struct {
	*httptest.Server
	mu     sync.Mutex
	body   map[string]any
	stream string
	status int
	maxCtx int
}

func newFake(t *testing.T, stream string) *fakeServer {
	f := &fakeServer{stream: stream, status: http.StatusOK, maxCtx: 262144}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			json.NewEncoder(w).Encode(map[string]any{
				"model_info":   map[string]any{"general.architecture": "qwen3", "qwen3.context_length": f.maxCtx},
				"capabilities": []string{"completion", "tools"},
			})
		case "/api/chat":
			data, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			json.Unmarshal(data, &f.body)
			f.mu.Unlock()
			w.WriteHeader(f.status)
			io.WriteString(w, f.stream)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func collect(t *testing.T, seq func(func(llm.StreamEvent, error) bool)) ([]llm.StreamEvent, error) {
	t.Helper()
	var evs []llm.StreamEvent
	var err error
	seq(func(e llm.StreamEvent, e2 error) bool {
		if e2 != nil {
			err = e2
			return false
		}
		evs = append(evs, e)
		return true
	})
	return evs, err
}

func TestStreamTextThinkingAndToolCall(t *testing.T) {
	f := newFake(t, strings.Join([]string{
		`{"message":{"role":"assistant","content":"","thinking":"let me look"},"done":false}`,
		`{"message":{"role":"assistant","content":"Checking."},"done":false}`,
		`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"glob","arguments":{"pattern":"*"}}}]},"done":false}`,
		`{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":1200,"eval_count":40}`,
	}, "\n"))
	p := New("ollama", f.URL+"/v1", 0)
	evs, err := collect(t, p.Stream(context.Background(), llm.Request{
		Model: "qwen3:4b", System: "sys", Effort: llm.EffortMax,
		Messages: []llm.Message{llm.UserText("list files")},
		Tools:    []llm.ToolSpec{{Name: "glob", Description: "find files", Schema: json.RawMessage(`{"type":"object"}`)}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	kinds := []llm.EventType{}
	for _, e := range evs {
		kinds = append(kinds, e.Type)
	}
	want := []llm.EventType{llm.EventThinkingDelta, llm.EventTextDelta, llm.EventToolUseStart, llm.EventDone}
	if len(kinds) != len(want) {
		t.Fatalf("events %v, want %v", kinds, want)
	}
	done := evs[len(evs)-1]
	if done.StopReason != llm.StopToolUse || done.Usage.Input != 1200 || done.Usage.Output != 40 {
		t.Fatalf("done: %+v", done)
	}
	uses := done.Message.ToolUses()
	if len(uses) != 1 || uses[0].Name != "glob" || string(uses[0].Input) != `{"pattern":"*"}` || uses[0].ID == "" {
		t.Fatalf("tool use: %+v", uses)
	}
	if done.Message.Blocks[0].Type != llm.BlockThinking || done.Message.Blocks[0].Provider != "ollama" {
		t.Fatalf("thinking block: %+v", done.Message.Blocks[0])
	}

	// The request asks for a usable window and maps effort to think.
	opts := f.body["options"].(map[string]any)
	if opts["num_ctx"] != float64(DefaultContextLength) {
		t.Errorf("num_ctx = %v, want %d", opts["num_ctx"], DefaultContextLength)
	}
	if f.body["think"] != "high" || f.body["stream"] != true {
		t.Errorf("think = %v, stream = %v", f.body["think"], f.body["stream"])
	}
	if msgs := f.body["messages"].([]any); msgs[0].(map[string]any)["role"] != "system" {
		t.Errorf("first message should be the system prompt: %v", msgs[0])
	}
}

func TestContextLengthConfiguredOrCapped(t *testing.T) {
	done := `{"message":{"content":"ok"},"done":true,"done_reason":"stop"}`
	f := newFake(t, done)
	f.maxCtx = 8192 // a model trained on a smaller window
	collect(t, New("ollama", f.URL, 0).Stream(context.Background(), llm.Request{Model: "small", Messages: []llm.Message{llm.UserText("hi")}}))
	if got := f.body["options"].(map[string]any)["num_ctx"]; got != float64(8192) {
		t.Errorf("default should be capped at the model's maximum, got %v", got)
	}
	collect(t, New("ollama", f.URL, 65536).Stream(context.Background(), llm.Request{Model: "small", Messages: []llm.Message{llm.UserText("hi")}}))
	if got := f.body["options"].(map[string]any)["num_ctx"]; got != float64(65536) {
		t.Errorf("a configured context_length wins, got %v", got)
	}
}

func TestMessagesNameToolResultsAndReplayThinking(t *testing.T) {
	p := New("ollama", "http://x", 0)
	msgs := p.messages(llm.Request{Model: "qwen3:4b", Messages: []llm.Message{
		llm.UserText("list"),
		{Role: llm.RoleAssistant, Model: "qwen3:4b", Blocks: []llm.Block{
			{Type: llm.BlockThinking, Text: "hmm", Provider: "ollama"},
			{Type: llm.BlockToolUse, ID: "call_0", Name: "glob", Input: json.RawMessage(`{"pattern":"*"}`)},
		}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ID: "call_0", Content: "a.go"}}},
		{Role: llm.RoleAssistant, Model: "other-model", Blocks: []llm.Block{{Type: llm.BlockThinking, Text: "not mine", Provider: "ollama"}, llm.TextBlock("done")}},
	}})
	if len(msgs) != 4 {
		t.Fatalf("got %d messages: %v", len(msgs), msgs)
	}
	asst := msgs[1]
	if asst["thinking"] != "hmm" {
		t.Errorf("thinking from the same model is replayed: %v", asst)
	}
	call := asst["tool_calls"].([]map[string]any)[0]["function"].(map[string]any)
	if call["name"] != "glob" || string(call["arguments"].(json.RawMessage)) != `{"pattern":"*"}` {
		t.Errorf("tool call: %v", call)
	}
	if tool := msgs[2]; tool["role"] != "tool" || tool["tool_name"] != "glob" || tool["content"] != "a.go" {
		t.Errorf("tool result should name its tool: %v", tool)
	}
	if _, ok := msgs[3]["thinking"]; ok {
		t.Errorf("another model's thinking must not be replayed: %v", msgs[3])
	}
}

func TestErrors(t *testing.T) {
	f := newFake(t, `{"error":"model 'nope' not found"}`)
	f.status = http.StatusNotFound
	_, err := collect(t, New("ollama", f.URL, 0).Stream(context.Background(), llm.Request{Model: "nope", Messages: []llm.Message{llm.UserText("hi")}}))
	var apiErr *llm.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 404 || !strings.Contains(err.Error(), "model 'nope' not found") {
		t.Fatalf("want a 404 API error with Ollama's message, got %v", err)
	}

	f.status, f.stream = http.StatusOK, `{"error":"out of memory"}`
	if _, err := collect(t, New("ollama", f.URL, 0).Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("hi")}})); err == nil || !strings.Contains(err.Error(), "out of memory") {
		t.Fatalf("a mid-stream error should surface, got %v", err)
	}

	f.stream = `{"message":{"content":"par"},"done":false}`
	if _, err := collect(t, New("ollama", f.URL, 0).Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("hi")}})); err == nil {
		t.Fatal("a stream that ends before done is an error")
	}
}

func TestSupportsTools(t *testing.T) {
	f := newFake(t, "")
	if ok, known := New("ollama", f.URL, 0).SupportsTools(context.Background(), "m"); !ok || !known {
		t.Fatalf("SupportsTools = %v, %v", ok, known)
	}
}

func TestToolResultImagesFollowAsUserMessage(t *testing.T) {
	p := New("ollama", "http://x", 0)
	msgs := p.messages(llm.Request{Messages: []llm.Message{
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ID: "c", Name: "browser_screenshot", Content: "Screenshot.",
			Images: []llm.Block{{Type: llm.BlockImage, MediaType: "image/png", Data: "AAAA"}}}}},
	}})
	if len(msgs) != 2 || msgs[1]["role"] != "user" {
		t.Fatalf("want the tool message, then a user message with the image: %v", msgs)
	}
	if imgs, _ := msgs[1]["images"].([]string); len(imgs) != 1 || imgs[0] != "AAAA" {
		t.Errorf("images: %v", msgs[1])
	}
}

// Sampling is an override: a request that configures none must look exactly
// as it did before sampling existed, so no existing setup changes behavior.
func TestNoSamplingSendsNoDecodingOptions(t *testing.T) {
	f := newFake(t, `{"message":{"role":"assistant","content":"hi"},"done":true,"done_reason":"stop"}`)
	p := New("ollama", f.URL+"/v1", 0)
	if _, err := collect(t, p.Stream(context.Background(), llm.Request{
		Model: "qwen3:4b", Messages: []llm.Message{llm.UserText("hello")},
	})); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	options, _ := f.body["options"].(map[string]any)
	f.mu.Unlock()
	for _, k := range []string{"temperature", "top_p", "top_k"} {
		if v, ok := options[k]; ok {
			t.Errorf("unconfigured request sent %s=%v", k, v)
		}
	}
	if _, ok := options["num_ctx"]; !ok {
		t.Error("num_ctx should still be sent")
	}
}

func TestSamplingReachesOllamaOptions(t *testing.T) {
	f := newFake(t, `{"message":{"role":"assistant","content":"hi"},"done":true,"done_reason":"stop"}`)
	p := New("ollama", f.URL+"/v1", 0)
	temp, topP, topK := 0.7, 0.8, 20
	if _, err := collect(t, p.Stream(context.Background(), llm.Request{
		Model: "qwen3:4b", Messages: []llm.Message{llm.UserText("hello")},
		Sampling: &llm.Sampling{Temperature: &temp, TopP: &topP, TopK: &topK},
	})); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	options, _ := f.body["options"].(map[string]any)
	f.mu.Unlock()
	// JSON numbers decode as float64, top_k included.
	for k, want := range map[string]float64{"temperature": 0.7, "top_p": 0.8, "top_k": 20} {
		if got, ok := options[k]; !ok || got != want {
			t.Errorf("options[%q] = %v (present %v), want %v", k, got, ok, want)
		}
	}
}

// A partial override sets only what it names and leaves the rest to Ollama.
func TestPartialSamplingSendsOnlyWhatIsSet(t *testing.T) {
	f := newFake(t, `{"message":{"role":"assistant","content":"hi"},"done":true,"done_reason":"stop"}`)
	p := New("ollama", f.URL+"/v1", 0)
	topK := 20
	if _, err := collect(t, p.Stream(context.Background(), llm.Request{
		Model: "qwen3:4b", Messages: []llm.Message{llm.UserText("hello")},
		Sampling: &llm.Sampling{TopK: &topK},
	})); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	options, _ := f.body["options"].(map[string]any)
	f.mu.Unlock()
	if options["top_k"] != float64(20) {
		t.Errorf("top_k = %v", options["top_k"])
	}
	if _, ok := options["temperature"]; ok {
		t.Error("temperature should be absent")
	}
}

// Ollama normally converts tool calls itself, but a model whose template is
// not applied leaves them in the text. Recover them there too.
func TestToolCallWrittenAsTextIsRecovered(t *testing.T) {
	f := newFake(t, `{"message":{"role":"assistant","content":"Reading it.\n<tool_call>{\"name\":\"read\",\"arguments\":{\"path\":\"a.go\"}}</tool_call>"},"done":true,"done_reason":"stop"}`)
	p := New("ollama", f.URL+"/v1", 0)
	evs, err := collect(t, p.Stream(context.Background(), llm.Request{
		Model:    "qwen3:4b",
		Messages: []llm.Message{llm.UserText("read a.go")},
		Tools:    []llm.ToolSpec{{Name: "read", Description: "d", Schema: json.RawMessage(`{"type":"object"}`)}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	done := evs[len(evs)-1]
	uses := done.Message.ToolUses()
	if len(uses) != 1 || uses[0].Name != "read" || string(uses[0].Input) != `{"path":"a.go"}` {
		t.Fatalf("tool uses = %+v", uses)
	}
	if done.StopReason != llm.StopToolUse {
		t.Errorf("stop reason = %q, want tool_use", done.StopReason)
	}
	if got := done.Message.Text(); got != "Reading it." {
		t.Errorf("text = %q, markup should be stripped", got)
	}
	var sawNotice bool
	for _, e := range evs {
		if e.Type == llm.EventNotice && strings.Contains(e.Text, "recovered a tool call") {
			sawNotice = true
		}
	}
	if !sawNotice {
		t.Error("the user should be told a call was recovered")
	}
}

// Nothing to salvage when the server did its job.
func TestStructuredToolCallsAreNotResalvaged(t *testing.T) {
	f := newFake(t, `{"message":{"role":"assistant","content":"like <tool_call>{\"name\":\"read\",\"arguments\":{\"path\":\"wrong\"}}</tool_call>","tool_calls":[{"function":{"name":"read","arguments":{"path":"right"}}}]},"done":true,"done_reason":"stop"}`)
	p := New("ollama", f.URL+"/v1", 0)
	evs, err := collect(t, p.Stream(context.Background(), llm.Request{
		Model:    "qwen3:4b",
		Messages: []llm.Message{llm.UserText("read")},
		Tools:    []llm.ToolSpec{{Name: "read", Description: "d", Schema: json.RawMessage(`{"type":"object"}`)}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	done := evs[len(evs)-1]
	if uses := done.Message.ToolUses(); len(uses) != 1 || string(uses[0].Input) != `{"path":"right"}` {
		t.Fatalf("the structured call must win: %+v", uses)
	}
	for _, e := range evs {
		if e.Type == llm.EventNotice {
			t.Errorf("unexpected notice %q", e.Text)
		}
	}
}
