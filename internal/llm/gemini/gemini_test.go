package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/llm/llmtest"
)

func TestStream(t *testing.T) {
	sig := base64.StdEncoding.EncodeToString([]byte("SIG"))
	body := llmtest.SSE(
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"Considering.","thought":true}]}}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"Let me "}]}}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"check."}]}}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"a.go"}},"thoughtSignature":"`+sig+`"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":80,"cachedContentTokenCount":30,"candidatesTokenCount":12,"thoughtsTokenCount":8}}`,
	)
	srv := llmtest.NewServer(t, 200, body)
	p := New("k", srv.URL)

	prior := llm.Message{Role: llm.RoleAssistant, Model: "gemini-3.8-flash", Blocks: []llm.Block{
		{Type: llm.BlockToolUse, ID: "gemini_call_0", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`), Signature: base64.StdEncoding.EncodeToString([]byte("PRIOR")), Provider: Name},
	}}
	req := llm.Request{
		Model:  "gemini-3.8-flash",
		System: "sys",
		Messages: []llm.Message{
			llm.UserText("go"),
			prior,
			{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ID: "gemini_call_0", Name: "bash", Content: "a.go"}}},
		},
		Tools:  []llm.ToolSpec{{Name: "read", Description: "d", Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)}},
		Effort: llm.EffortLow,
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
	if done.Message.Text() != "Let me check." {
		t.Errorf("text = %q", done.Message.Text())
	}
	uses := done.Message.ToolUses()
	if len(uses) != 1 || uses[0].Name != "read" || !strings.HasPrefix(uses[0].ID, "gemini_call_") || geminiID(uses[0].ID) != "" || uses[0].Signature != sig {
		t.Fatalf("uses = %+v", uses)
	}
	if done.StopReason != llm.StopToolUse || done.Usage.Input != 50 || done.Usage.CacheRead != 30 || done.Usage.Output != 20 {
		t.Errorf("done = %+v", done)
	}
	sent := srv.LastBody()
	prior64 := base64.StdEncoding.EncodeToString([]byte("PRIOR"))
	for _, want := range []string{`"functionResponse"`, `"thoughtSignature":"` + prior64 + `"`, `"systemInstruction"`, `"thinkingLevel":"LOW"`, `"includeThoughts":true`, `"parametersJsonSchema"`} {
		if !strings.Contains(sent, want) {
			t.Errorf("request missing %s:\n%s", want, sent)
		}
	}
	if strings.Contains(sent, "gemini_call_") {
		t.Error("synthesized call ids leaked to the API")
	}
}

func TestToolResultImagesAreSiblingParts(t *testing.T) {
	cs := contents(llm.Request{Messages: []llm.Message{
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ID: "c", Name: "browser_screenshot", Content: "Screenshot.",
			Images: []llm.Block{{Type: llm.BlockImage, MediaType: "image/png", Data: "AAAA"}}}}},
	}})
	if len(cs) != 1 || len(cs[0].Parts) != 2 {
		t.Fatalf("want a function response and an image part: %+v", cs)
	}
	if cs[0].Parts[0].FunctionResponse == nil || cs[0].Parts[1].InlineData == nil || cs[0].Parts[1].InlineData.MIMEType != "image/png" || len(cs[0].Parts[1].InlineData.Data) != 3 {
		t.Errorf("parts: %+v %+v", cs[0].Parts[0], cs[0].Parts[1])
	}
}
