package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/trace"
)

func TestTraceRecordsATurn(t *testing.T) {
	a, _, dir := setup(t, permission.ModeDefault,
		assistant(toolUse("c1", "write", `{"path":"a.txt","content":"hi"}`)),
		assistant(llm.TextBlock("done")),
	)
	tdir := filepath.Join(dir, "t.trace")
	rec, err := trace.Open(tdir)
	if err != nil {
		t.Fatal(err)
	}
	a.SetTrace(rec.Tracer())
	drain(a.Run(t.Context(), "make a.txt"), PermissionReply{Allow: true})
	a.SetTrace(nil)
	rec.Close()

	data, _ := os.ReadFile(filepath.Join(tdir, trace.EventsFile))
	var kinds []string
	var recs []trace.Record
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var r trace.Record
		json.Unmarshal([]byte(line), &r)
		recs = append(recs, r)
		kinds = append(kinds, r.Kind)
	}
	want := "state turn_start request response permission tool_start tool_end request response turn_end state"
	if got := strings.Join(kinds, " "); got != want {
		t.Fatalf("records:\n got %s\nwant %s", got, want)
	}
	resp := recs[3].Data.(map[string]any)
	if recs[3].Req != recs[2].Req || resp["ttft_ms"] == nil || resp["message"] == nil {
		t.Errorf("response: %+v", recs[3])
	}
	perm := recs[4].Data.(map[string]any)
	if perm["decision"] != "allow" || perm["tool"] != "write" {
		t.Errorf("permission: %v", perm)
	}
	second := recs[7].Data.(map[string]any)
	if second["from"].(float64) != 1 || len(second["messages"].([]any)) != 2 {
		t.Errorf("the second request should add the reply and tool result only: %v", second)
	}
	if recs[9].Data.(map[string]any)["stop"] != "end_turn" {
		t.Errorf("turn end: %v", recs[9].Data)
	}
}

func TestSubagentTracesInItsOwnLane(t *testing.T) {
	a, _, dir := setup(t, permission.ModeDefault)
	rec, _ := trace.Open(filepath.Join(dir, "t.trace"))
	defer rec.Close()
	a.SetTrace(rec.Tracer())
	child := a.Spawn(SpawnOptions{Type: "explore", Label: "explore: look", TraceParent: "call_9", Provider: &fakeProvider{script: []llm.Message{assistant(llm.TextBlock("found"))}}, Model: "m", Tools: a.Tools()})
	for range child.Run(t.Context(), "look around") {
	}
	data, _ := os.ReadFile(filepath.Join(dir, "t.trace", trace.EventsFile))
	if !strings.Contains(string(data), `"agent":"explore: look","parent":"call_9","req"`) {
		t.Fatalf("the child's request should be in its lane:\n%s", data)
	}
}
