package desktop

import (
	"strings"
	"testing"
)

func TestAgentHookIdentityAndResumeCommands(t *testing.T) {
	for _, tc := range []struct {
		kind, event, payload, command string
	}{
		{"claude", "Stop", `{"session_id":"123e4567-e89b-12d3-a456-426614174000","cwd":"/work","prompt":"SECRET"}`, "claude --resume 123e4567-e89b-12d3-a456-426614174000"},
		{"codex", "Stop", `{"session_id":"123e4567-e89b-12d3-a456-426614174000","cwd":"/work","tool_input":"SECRET"}`, "codex resume 123e4567-e89b-12d3-a456-426614174000"},
		{"opencode", "session.idle", `{"properties":{"sessionID":"ses_12345678"},"cwd":"/work","prompt":"SECRET"}`, "opencode --session ses_12345678"},
		{"gemini", "AfterAgent", `{"session_id":"123e4567-e89b-12d3-a456-426614174000","cwd":"/work","prompt_response":"SECRET"}`, "gemini --resume 123e4567-e89b-12d3-a456-426614174000"},
	} {
		req, ok := HookRequest(tc.kind, tc.event, "tab-1", []byte(tc.payload))
		if !ok || req.Agent == nil || !req.Agent.valid() {
			t.Fatalf("%s identity: %+v %v", tc.kind, req.Agent, ok)
		}
		got, err := req.Agent.command(false)
		if err != nil || got != tc.command || strings.Contains(got, "SECRET") {
			t.Fatalf("%s command: %q, %v", tc.kind, got, err)
		}
	}
	for _, id := range []string{"", "../other", "id;rm -rf /", "id with spaces", "🚨"} {
		if (agentResume{Kind: "claude", ID: id, Tab: "tab", Dir: "/work"}).valid() {
			t.Fatalf("unsafe agent ID accepted: %q", id)
		}
	}
	if got := agentForTab(&tab{id: "tab", dir: remotePath("server", "/work")}, agentResume{Kind: "claude", ID: "12345678", Tab: "tab", Dir: "/work"}); got.Dir != remotePath("server", "/work") {
		t.Fatalf("remote agent directory: %+v", got)
	}
}

func TestHostKeepsAgentIdentityWithoutPrompt(t *testing.T) {
	needTerminal(t)
	_, connect := standIn(t, t.TempDir())
	dial, _, stop, _, err := connect("devbox")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	dir := t.TempDir()
	conn, _, _, err := askHostVia(dial, hostRequest{Op: "open", ID: "agent-tab", Shell: true, Dir: dir, Command: []string{"/bin/sh", "-c", "sleep 4"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	identity := agentResume{Kind: "claude", ID: "123e4567-e89b-12d3-a456-426614174000", Tab: "agent-tab", Dir: dir}
	statusConn, _, _, err := askHostVia(dial, hostRequest{Op: "status", ID: "agent-tab", Status: "id=claude:state=working", Agent: &identity})
	if err != nil {
		t.Fatal(err)
	}
	statusConn.Close()
	listConn, _, reply, err := askHostVia(dial, hostRequest{Op: "list"})
	if err != nil {
		t.Fatal(err)
	}
	listConn.Close()
	if len(reply.Tabs) != 1 || reply.Tabs[0].Agent != identity {
		t.Fatalf("host agent metadata: %+v", reply.Tabs)
	}
	bad := identity
	bad.ID = "../other"
	badConn, _, _, err := askHostVia(dial, hostRequest{Op: "status", ID: "agent-tab", Status: "id=claude:state=done", Agent: &bad})
	if err != nil {
		t.Fatal(err)
	}
	badConn.Close()
	listConn, _, reply, err = askHostVia(dial, hostRequest{Op: "list"})
	if err != nil {
		t.Fatal(err)
	}
	listConn.Close()
	if reply.Tabs[0].Agent != identity {
		t.Fatal("malformed session ID replaced the valid one")
	}
}
