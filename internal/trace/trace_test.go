package trace

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"larik/internal/llm"
)

func readAll(t *testing.T, dir string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestNilTracerRecordsNothing(t *testing.T) {
	var tr *Tracer
	tr.TurnStart("x", "x", nil)
	if id := tr.Request("p", llm.Request{}, ""); id != "" {
		t.Fatal("a nil tracer has no request ids")
	}
	tr.Response("", Response{})
	tr.RecordHTTP(&llm.WireExchange{})
	tr.Child("a", "b").Note(KindNotice, "x")
	if tr.Wire(t.Context(), "r1") != t.Context() {
		t.Fatal("a nil tracer shouldn't tag the context")
	}
}

func TestRequestDeltas(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s.trace")
	rec, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	tr := rec.Tracer()
	tools := []llm.ToolSpec{{Name: "read"}}
	m1, m2, m3 := llm.UserText("one"), llm.UserText("two"), llm.UserText("three")
	tr.Request("fake", llm.Request{Model: "m", System: "sys", Tools: tools, Messages: []llm.Message{m1}}, "")
	tr.Request("fake", llm.Request{Model: "m", System: "sys", Tools: tools, Messages: []llm.Message{m1, m2, m3}}, "")
	// A rewritten context (compaction) is sent whole.
	tr.Request("fake", llm.Request{Model: "m", System: "sys2", Tools: tools, Messages: []llm.Message{m3}}, "compaction")
	child := tr.Child("explore: x", "call_1")
	child.Request("fake", llm.Request{Model: "small", System: "sub", Messages: []llm.Message{m1}}, "")
	rec.Close()

	recs := readAll(t, dir)
	if len(recs) != 4 {
		t.Fatalf("%d records", len(recs))
	}
	d := func(i int) map[string]any { return recs[i]["data"].(map[string]any) }
	if d(0)["system"] != "sys" || d(0)["tools"] == nil || len(d(0)["messages"].([]any)) != 1 || d(0)["reset"] != nil {
		t.Errorf("first request should carry everything: %v", d(0))
	}
	if _, ok := d(1)["system"]; ok {
		t.Error("an unchanged system prompt shouldn't be written again")
	}
	if d(1)["tools"] != nil || d(1)["tool_names"] == nil {
		t.Error("unchanged tools are named, not written again")
	}
	if d(1)["from"].(float64) != 1 || len(d(1)["messages"].([]any)) != 2 {
		t.Errorf("second request should send only the new messages: %v", d(1))
	}
	if d(2)["reset"] != true || d(2)["system"] != "sys2" || d(2)["purpose"] != "compaction" {
		t.Errorf("a rewritten context should reset: %v", d(2))
	}
	if recs[3]["agent"] != "explore: x" || recs[3]["parent"] != "call_1" || d(3)["from"].(float64) != 0 {
		t.Errorf("a subagent keeps its own context: %v", recs[3])
	}
	for i := 1; i < len(recs); i++ {
		if recs[i]["seq"].(float64) <= recs[i-1]["seq"].(float64) {
			t.Fatal("seq must increase")
		}
	}
	if recs[0]["req"] == recs[1]["req"] {
		t.Fatal("request ids must differ")
	}

	fi, _ := os.Stat(filepath.Join(dir, EventsFile))
	di, _ := os.Stat(dir)
	if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
		t.Errorf("modes: file %v dir %v", fi.Mode(), di.Mode())
	}

	// Reopening continues numbering.
	rec, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := rec.Tracer().Request("fake", llm.Request{}, "")
	rec.Close()
	all := readAll(t, dir)
	last := all[len(all)-1]
	if last["seq"].(float64) != 5 || last["req"] != id || id == recs[0]["req"] {
		t.Errorf("reopened trace should continue: %v", last)
	}
}

func TestRecordHTTPWritesBodies(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s.trace")
	rec, _ := Open(dir)
	tr := rec.Tracer()
	now := time.Now()
	tr.RecordHTTP(&llm.WireExchange{Req: "r1", Method: "POST", URL: "https://api/x", ReqHeader: http.Header{"A": {"b"}},
		ReqBody: []byte(`{"q":1}`), Status: 200, RespBody: []byte("data: hi\n\n"), Sent: now, Headers: now, Done: now})
	rec.Close()
	d := readAll(t, dir)[0]["data"].(map[string]any)
	req, _ := os.ReadFile(filepath.Join(dir, "http", d["request_body"].(string)))
	resp, _ := os.ReadFile(filepath.Join(dir, "http", d["response_body"].(string)))
	if string(req) != `{"q":1}` || string(resp) != "data: hi\n\n" || d["status"].(float64) != 200 {
		t.Errorf("bodies: %q %q %v", req, resp, d)
	}
}

func TestPrune(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "p-1")
	old, fresh := filepath.Join(proj, "a.trace"), filepath.Join(proj, "b.trace")
	for _, d := range []string{old, fresh} {
		os.MkdirAll(d, 0o700)
		os.WriteFile(filepath.Join(d, EventsFile), []byte("{}\n"), 0o600)
	}
	os.WriteFile(filepath.Join(proj, "a.jsonl"), nil, 0o600)
	past := time.Now().Add(-30 * 24 * time.Hour)
	os.Chtimes(filepath.Join(old, EventsFile), past, past)
	if err := Prune(root, 14*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the old trace should be gone")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("the fresh trace should stay")
	}
	if _, err := os.Stat(filepath.Join(proj, "a.jsonl")); err != nil {
		t.Error("sessions themselves aren't pruned")
	}
}
