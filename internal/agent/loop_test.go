package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/session"
	"larik/internal/tools"
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

// loopRun repeats one glob call enough times for the guard to trip and
// reports how the turn ended and how many loop notices it showed.
func loopRun(t *testing.T, mode permission.Mode, unattended bool) (stop string, notices int) {
	t.Helper()
	var script []llm.Message
	for range 6 {
		script = append(script, assistant(toolUse("t", "glob", `{"pattern":"*.none"}`)))
	}
	script = append(script, assistant(llm.TextBlock("done")))
	a, _, _ := setup(t, mode, script...)
	a.opts.Unattended = unattended
	for _, e := range drain(a.Run(context.Background(), "go"), PermissionReply{Allow: true}) {
		switch {
		case e.Kind == EvDone:
			stop = e.StopReason
		case e.Kind == EvNotice && strings.Contains(e.Text, "press Esc"):
			notices++
		}
	}
	return stop, notices
}

func TestRootAgentLoopGuard(t *testing.T) {
	if stop, _ := loopRun(t, permission.ModeDefault, true); stop != "loop" {
		t.Errorf("unattended: stop = %q, want loop", stop)
	}
	if stop, _ := loopRun(t, permission.ModeYolo, false); stop != "loop" {
		t.Errorf("yolo: stop = %q, want loop", stop)
	}
	stop, notices := loopRun(t, permission.ModeDefault, false)
	if stop != string(llm.StopEnd) {
		t.Errorf("interactive: stop = %q; the user can interrupt, so the turn continues", stop)
	}
	if notices != 1 {
		t.Errorf("interactive: %d loop notices, want exactly 1", notices)
	}
}

func TestAuthorizationDecisionsAreLogged(t *testing.T) {
	dir := t.TempDir()
	fp := &fakeProvider{script: []llm.Message{
		assistant(toolUse("r1", "glob", `{"pattern":"*.none"}`), toolUse("w1", "write", `{"path":"a.txt","content":"x"}`), toolUse("w2", "write", `{"path":"b.txt","content":"y"}`)),
		assistant(llm.TextBlock("done")),
	}}
	sess, err := session.Create(filepath.Join(dir, "sessions"), session.Meta{Cwd: dir, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	a := New(Options{Provider: fp, Model: "m", Cwd: dir, Tools: tools.Default(), Session: sess,
		Perms: permission.NewChecker(permission.ModeDefault, permission.Rules{Deny: []string{"write(b.txt)"}}, dir)})
	drain(a.Run(context.Background(), "go"), PermissionReply{Allow: true})

	data, err := os.ReadFile(sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e session.Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		if e.Type == session.EntryDecision {
			got[e.Decision.ToolID] = e.Decision.Answer
		}
	}
	if _, ok := got["r1"]; ok {
		t.Error("a read-only call allowed by mode has no decision worth logging")
	}
	if got["w1"] != "allow" {
		t.Errorf("approved write: %q", got["w1"])
	}
	if got["w2"] != "deny (rule)" {
		t.Errorf("rule-denied write: %q", got["w2"])
	}
}

// recorder collects what an Observer is told.
type recorder struct {
	mu   sync.Mutex
	seen []observed
}

type observed struct {
	from Origin
	e    Event
}

func (r *recorder) Observe(from Origin, e Event) {
	r.mu.Lock()
	r.seen = append(r.seen, observed{from, e})
	r.mu.Unlock()
}

func TestObserverSeesEventsButNotDeltas(t *testing.T) {
	a, _, _ := setup(t, permission.ModeDefault,
		assistant(toolUse("t1", "glob", `{"pattern":"*.none"}`)), assistant(llm.TextBlock("done")))
	r := &recorder{}
	a.opts.Observer = r
	drain(a.Run(context.Background(), "go"), PermissionReply{Allow: true})

	var kinds []EventKind
	for _, o := range r.seen {
		if o.from != (Origin{}) {
			t.Errorf("the main agent has no label: %+v", o.from)
		}
		kinds = append(kinds, o.e.Kind)
	}
	want := map[EventKind]bool{EvToolStart: false, EvToolEnd: false, EvUsage: false, EvDone: false}
	for _, k := range kinds {
		if k == EvTextDelta || k == EvThinkingDelta || k == EvToolCallDelta {
			t.Errorf("deltas are display-only and must not reach observers: %v", kinds)
		}
		if _, ok := want[k]; ok {
			want[k] = true
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("observer never saw %s: %v", k, kinds)
		}
	}
	if last := r.seen[len(r.seen)-1]; last.e.Kind != EvDone || last.e.StopReason != string(llm.StopEnd) {
		t.Errorf("the turn ends with done and its stop reason: %+v", last)
	}
}

func TestSubagentEventsCarryTheirOrigin(t *testing.T) {
	parent, _, _ := setup(t, permission.ModeDefault)
	r := &recorder{}
	parent.opts.Observer = r
	child := parent.Spawn(SpawnOptions{Type: "worker", Label: "worker: x", TraceParent: "task-7",
		Provider: &fakeProvider{script: []llm.Message{assistant(llm.TextBlock("hi"))}}, Model: "m", Tools: tools.NewRegistry()})
	for range child.Run(context.Background(), "go") {
	}
	if len(r.seen) == 0 {
		t.Fatal("a subagent inherits its parent's observer")
	}
	for _, o := range r.seen {
		if o.from != (Origin{Agent: "worker: x", Parent: "task-7"}) {
			t.Errorf("origin = %+v", o.from)
		}
	}
}
