package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"larik/internal/llm"
	"larik/internal/permission"
)

func call(name, input string) []llm.Block {
	return []llm.Block{{Type: llm.BlockToolUse, ID: "x", Name: name, Input: json.RawMessage(input)}}
}

func result(s string) []llm.Block { return []llm.Block{{Type: llm.BlockToolResult, Content: s}} }

func TestLoopGuard(t *testing.T) {
	var g loopGuard
	for i := range 3 {
		if g.see(call("bash", `{"command":"echo done"}`), result("done")) {
			t.Fatalf("stuck after %d repeats", i+1)
		}
	}
	if !g.see(call("bash", `{"command":"echo done"}`), result("done")) {
		t.Error("4 identical turns should count as stuck")
	}

	// Rerunning tests between edits: same call, new results each time.
	g = loopGuard{}
	for i := range 8 {
		if g.see(call("bash", `{"command":"go test ./..."}`), result(fmt.Sprintf("FAIL %d", i))) ||
			g.see(call("edit", fmt.Sprintf(`{"n":%d}`, i)), result("ok")) {
			t.Fatalf("honest retries flagged at round %d", i)
		}
	}

	// A two-step cycle is caught too.
	g = loopGuard{}
	stuck := false
	for range 4 {
		stuck = g.see(call("bash", `{"command":"rm -rf x"}`), result("")) || g.see(call("bash", `{"command":"ls x"}`), result("gone"))
	}
	if !stuck {
		t.Error("an rm/ls cycle should be caught within the window")
	}
}

func TestMainAgentIsNotLoopGuarded(t *testing.T) {
	var script []llm.Message
	for range 6 {
		script = append(script, assistant(toolUse("t", "glob", `{"pattern":"*.none"}`)))
	}
	script = append(script, assistant(llm.TextBlock("done")))
	a, _, _ := setup(t, permission.ModeDefault, script...)
	var stop string
	for _, e := range drain(a.Run(context.Background(), "go"), PermissionReply{Allow: true}) {
		if e.Kind == EvDone {
			stop = e.StopReason
		}
	}
	if stop != string(llm.StopEnd) {
		t.Errorf("stop = %q; the user watches the main agent and can interrupt it", stop)
	}
}
