package desktop

import (
	"strings"
	"testing"
)

func TestHookRequestMapsEventsWithoutLeakingInput(t *testing.T) {
	cases := []struct{ agent, event, payload, state, kind string }{
		{"claude", "UserPromptSubmit", `{"prompt":"SECRET"}`, "working", ""},
		{"claude", "PermissionRequest", `{"tool_input":{"command":"SECRET"}}`, "blocked", "permission"},
		{"claude", "Stop", `{}`, "done", ""},
		{"claude", "StopFailure", `{}`, "error", ""},
		{"codex", "UserPromptSubmit", `{}`, "working", ""},
		{"codex", "PermissionRequest", `{}`, "blocked", "permission"},
		{"codex", "Stop", `{}`, "done", ""},
		{"opencode", "session.status", `{"properties":{"status":{"type":"busy"}}}`, "working", ""},
		{"opencode", "permission.asked", `{}`, "blocked", "permission"},
		{"opencode", "permission.replied", `{}`, "working", ""},
		{"opencode", "session.idle", `{}`, "done", ""},
		{"opencode", "session.error", `{}`, "error", ""},
		{"gemini", "BeforeAgent", `{}`, "working", ""},
		{"gemini", "Notification", `{"notification_type":"ToolPermission"}`, "blocked", "permission"},
		{"gemini", "AfterTool", `{}`, "working", ""},
		{"gemini", "AfterAgent", `{}`, "done", ""},
	}
	for _, tc := range cases {
		req, ok := HookRequest(tc.agent, tc.event, "tab-1", []byte(tc.payload))
		if !ok || req.State != tc.state || req.Kind != tc.kind || req.Tab != "tab-1" {
			t.Fatalf("%s %s: %+v %v", tc.agent, tc.event, req, ok)
		}
		if strings.Contains(req.Message, "SECRET") || strings.Contains(req.Title, "SECRET") {
			t.Fatal("hook payload leaked into status")
		}
	}
	if _, ok := HookRequest("claude", "Stop", "", []byte(`{}`)); ok {
		t.Fatal("hook outside Desktop reported a status")
	}
	if _, ok := HookRequest("codex", "SessionStart", "tab-1", []byte(`{}`)); ok {
		t.Fatal("unobserved event inferred a state")
	}
}
