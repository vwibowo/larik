package openai

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
	p := New("", "k", srv.URL)

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

func TestOrphanedToolCallGetsSyntheticOutput(t *testing.T) {
	req := llm.Request{Model: "gpt-5.5", Messages: []llm.Message{
		llm.UserText("start"),
		{Role: llm.RoleAssistant, Model: "claude-opus-5", Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "toolu_orphan", Name: "bash", Input: json.RawMessage(`{"command":"pwd"}`)}}},
		llm.UserText("continue"),
	}}
	got, err := json.Marshal(inputItems(req, "codex"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(got)
	call := strings.Index(body, `"type":"function_call"`)
	result := strings.Index(body, `"type":"function_call_output"`)
	prompt := strings.LastIndex(body, `"text":"continue"`)
	if call < 0 || result < call || prompt < result || !strings.Contains(body, `"call_id":"toolu_orphan"`) || !strings.Contains(body, "tool call did not produce a result") {
		t.Fatalf("orphan was not repaired in order: %s", body)
	}
}

func TestReasoningModel(t *testing.T) {
	for m, want := range map[string]bool{"gpt-5.5": true, "gpt-6-sol": true, "o3": true, "gpt-4.1": false, "gpt-5.1-chat-latest": false} {
		if got := reasoningModel(m); got != want {
			t.Errorf("%s: got %v", m, got)
		}
	}
}

func TestChatGPTHeadersAndParams(t *testing.T) {
	var got *http.Request
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		got, body = r, string(data)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, llmtest.SSE(`{"type":"response.completed","sequence_number":1,"response":{"id":"r","object":"response","status":"completed","model":"gpt-6-luna","output":[{"id":"m","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hi","annotations":[]}]}],"usage":{"input_tokens":5,"input_tokens_details":{"cached_tokens":0},"output_tokens":1,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":6}}}`))
	}))
	defer srv.Close()
	p := NewChatGPT("codex", srv.URL, func(context.Context) (string, string, error) { return "tok_1", "acct_1", nil })
	var done llm.StreamEvent
	const key = "0d6c7f2e-5a3b-4c1d-9e8f-1a2b3c4d5e6f"
	for ev, err := range p.Stream(context.Background(), llm.Request{Model: "gpt-6-luna", System: "sys", MaxTokens: 100, Messages: []llm.Message{llm.UserText("hi")}, CacheKey: key}) {
		if err != nil {
			t.Fatal(err)
		}
		done = ev
	}
	if done.Message.Text() != "hi" || p.Name() != "codex" {
		t.Fatalf("done %+v name %q", done, p.Name())
	}
	if got.Header.Get("Authorization") != "Bearer tok_1" || got.Header.Get("chatgpt-account-id") != "acct_1" || got.URL.Path != "/responses" {
		t.Fatalf("request %s headers %v", got.URL.Path, got.Header)
	}
	if strings.Contains(body, "max_output_tokens") || !strings.Contains(body, `"store":false`) {
		t.Fatalf("body: %s", body)
	}
	// The backend takes the cache key from these headers, not the body.
	if got.Header.Get("session_id") != key || got.Header.Get("conversation_id") != key || !strings.Contains(body, `"prompt_cache_key":"`+key+`"`) {
		t.Fatalf("cache key not sent: headers %v body %s", got.Header, body)
	}
}

// The ChatGPT backend streams finished items but completes with an empty
// output list; the answer must come from the streamed items.
func TestOutputFromStreamedItems(t *testing.T) {
	call := `{"id":"fc_1","type":"function_call","call_id":"call_1","name":"glob","arguments":"{\"pattern\":\"*\"}","status":"completed"}`
	msg := `{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Looking.","annotations":[]}]}`
	srv := llmtest.NewServer(t, 200, llmtest.SSE(
		`{"type":"response.output_item.done","output_index":0,"item":`+msg+`,"sequence_number":1}`,
		`{"type":"response.output_item.done","output_index":1,"item":`+call+`,"sequence_number":2}`,
		`{"type":"response.completed","sequence_number":3,"response":{"id":"r","object":"response","status":"completed","model":"gpt-6-luna","output":[],"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":0},"output_tokens":5,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":15}}}`,
	))
	p := NewChatGPT("codex", srv.URL, func(context.Context) (string, string, error) { return "t", "a", nil })
	var done llm.StreamEvent
	for ev, err := range p.Stream(context.Background(), llm.Request{Model: "gpt-6-luna", Messages: []llm.Message{llm.UserText("go")}}) {
		if err != nil {
			t.Fatal(err)
		}
		done = ev
	}
	if done.Message.Text() != "Looking." || len(done.Message.ToolUses()) != 1 || done.StopReason != llm.StopToolUse {
		t.Fatalf("answer lost: %+v", done)
	}
}

func TestToolResultImages(t *testing.T) {
	items := inputItems(llm.Request{Messages: []llm.Message{
		{Role: llm.RoleUser, Blocks: []llm.Block{
			{Type: llm.BlockToolResult, ID: "call_1", Content: "plain"},
			{Type: llm.BlockToolResult, ID: "call_2", Content: "Screenshot.", Images: []llm.Block{{Type: llm.BlockImage, MediaType: "image/png", Data: "AAAA"}}},
		}},
	}}, "openai")
	got, _ := json.Marshal(items)
	want := `[{"call_id":"call_1","output":"plain","type":"function_call_output"},` +
		`{"call_id":"call_2","output":[{"text":"Screenshot.","type":"input_text"},{"image_url":"data:image/png;base64,AAAA","type":"input_image"}],"type":"function_call_output"}]`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// A PDF goes to the Responses API as an input_file carrying a data URL and
// a filename, which the API requires.
func TestDocumentBecomesAnInputFile(t *testing.T) {
	items := inputItems(llm.Request{Messages: []llm.Message{
		{Role: llm.RoleUser, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "summarize this"},
			{Type: llm.BlockDocument, MediaType: "application/pdf", Data: "JVBERi0=", Pages: 3, Attachment: "docs/spec.pdf"},
		}},
	}}, "openai")
	got, _ := json.Marshal(items)
	want := `[{"content":[{"text":"summarize this","type":"input_text"},` +
		`{"file_data":"data:application/pdf;base64,JVBERi0=","filename":"spec.pdf","type":"input_file"}],"role":"user"}]`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestDocumentWithoutAPathStillSendsAFilename(t *testing.T) {
	items := inputItems(llm.Request{Messages: []llm.Message{
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockDocument, MediaType: "application/pdf", Data: "AA=="}}},
	}}, "openai")
	got, _ := json.Marshal(items)
	if !strings.Contains(string(got), `"filename":"attachment.pdf"`) {
		t.Errorf("want a fallback filename, got %s", got)
	}
}

func TestProviderDeclaresPDFSupport(t *testing.T) {
	p := New("", "k", "")
	if !llm.AcceptsDocuments(p, "application/pdf") {
		t.Error("the Responses API takes PDFs")
	}
	if llm.AcceptsDocuments(p, "application/zip") {
		t.Error("only PDF should be claimed")
	}
}
