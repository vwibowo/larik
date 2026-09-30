package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"larik/internal/agent"
	"larik/internal/checkpoint"
	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/session"
	"larik/internal/tools"
)

// funcProvider answers each request with respond(req), so parallel
// children get deterministic replies regardless of scheduling.
type funcProvider struct {
	respond func(req llm.Request) llm.Message
	// gate, if set, blocks child requests until it closes or ctx ends,
	// the way a slow real provider would honor cancellation.
	gate chan struct{}
	mu   sync.Mutex
	reqs []llm.Request
}

func (f *funcProvider) Name() string { return "fake" }
func (f *funcProvider) Stream(ctx context.Context, req llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		f.mu.Lock()
		f.reqs = append(f.reqs, req)
		f.mu.Unlock()
		if f.gate != nil && isChild(req) {
			select {
			case <-f.gate:
			case <-ctx.Done():
				yield(llm.StreamEvent{}, ctx.Err())
				return
			}
		}
		msg := f.respond(req)
		msg.Role, msg.Model = llm.RoleAssistant, req.Model
		stop := llm.StopEnd
		if len(msg.ToolUses()) > 0 {
			stop = llm.StopToolUse
		}
		yield(llm.StreamEvent{Type: llm.EventDone, Message: msg, StopReason: stop, Usage: llm.Usage{Input: 10, Output: 5}}, nil)
	}
}

func (f *funcProvider) requests() []llm.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]llm.Request(nil), f.reqs...)
}

// parentRequests leaves out subagents' requests, which interleave with the
// parent's when a background child is running.
func (f *funcProvider) parentRequests() []llm.Request {
	var out []llm.Request
	for _, r := range f.requests() {
		if !isChild(r) {
			out = append(out, r)
		}
	}
	return out
}

func use(id, name, input string) llm.Block {
	return llm.Block{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}
}

func text(s string) llm.Message { return llm.Message{Blocks: []llm.Block{llm.TextBlock(s)}} }

func toolResults(req llm.Request) []llm.Block {
	last := req.Messages[len(req.Messages)-1]
	var out []llm.Block
	for _, b := range last.Blocks {
		if b.Type == llm.BlockToolResult {
			out = append(out, b)
		}
	}
	return out
}

func isChild(req llm.Request) bool { return strings.Contains(req.System, footer) }

func newParent(t *testing.T, fp *funcProvider, mode permission.Mode) (*agent.Agent, string, *session.Session) {
	t.Helper()
	dir := t.TempDir()
	sess, err := session.Create(filepath.Join(dir, "sessions"), session.Meta{Cwd: dir, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	task := &Tool{Set: Discover(nil), Context: "<env>test</env>"}
	a := agent.New(agent.Options{
		Provider:    fp,
		Model:       "m",
		Cwd:         dir,
		Tools:       tools.NewRegistry(append(tools.Builtin(), task)...),
		Perms:       permission.NewChecker(mode, permission.Rules{}, dir),
		Session:     sess,
		Checkpoints: checkpoint.New(filepath.Join(dir, "ckpt"), dir),
	})
	return a, dir, sess
}

func drain(ch <-chan agent.Event, allow bool) []agent.Event {
	var evs []agent.Event
	for e := range ch {
		if e.Kind == agent.EvPermission {
			e.Reply <- agent.PermissionReply{Allow: allow}
		}
		evs = append(evs, e)
	}
	return evs
}

func TestExploreSubagent(t *testing.T) {
	fp := &funcProvider{}
	fp.respond = func(req llm.Request) llm.Message {
		if isChild(req) {
			if res := toolResults(req); len(res) > 0 {
				return text("Found it in main.go:1 — " + strings.TrimSpace(res[0].Content))
			}
			return llm.Message{Blocks: []llm.Block{use("c1", "grep", `{"pattern":"package"}`)}}
		}
		if res := toolResults(req); len(res) > 0 {
			return text("Parent summary: " + res[0].Content)
		}
		return llm.Message{Blocks: []llm.Block{use("p1", "task", `{"description":"find package","prompt":"Where is the package clause?","subagent_type":"explore"}`)}}
	}
	a, dir, sess := newParent(t, fp, permission.ModeDefault)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)

	evs := drain(a.Run(context.Background(), "find the package clause"), false)

	reqs := fp.requests()
	var child llm.Request
	for _, r := range reqs {
		if isChild(r) {
			child = r
			break
		}
	}
	if !strings.Contains(child.System, "read-only search subagent") || !strings.Contains(child.System, "<env>test</env>") {
		t.Errorf("child system prompt:\n%s", child.System)
	}
	var names []string
	for _, s := range child.Tools {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, ","); got != "read,grep,glob" { // lsp isn't registered in this test
		t.Errorf("explore tools = %s", got)
	}
	if child.Messages[0].Text() != "Where is the package clause?" {
		t.Errorf("child prompt = %q", child.Messages[0].Text())
	}

	final := reqs[len(reqs)-1]
	res := toolResults(final)
	if len(res) != 1 || res[0].IsError || !strings.Contains(res[0].Content, "Found it in main.go:1") {
		t.Fatalf("task result = %+v", res)
	}

	forwarded := 0
	for _, e := range evs {
		if e.Kind == agent.EvToolStart && e.Agent == "explore: find package" && e.ToolName == "grep" {
			forwarded++
		}
	}
	if forwarded != 1 {
		t.Errorf("child grep not forwarded with label")
	}

	// Child spend rolls into the parent: 2 parent calls + 2 child calls.
	if st := a.Stats(); st.Total.Input != 40 {
		t.Errorf("total input = %d, want 40", st.Total.Input)
	}
	sess.Close()
	_, state, err := session.Open(sess.Path)
	if err != nil || state.Usage.Input != 40 {
		t.Errorf("resumed usage = %+v err=%v", state.Usage, err)
	}
	if _, err := os.Stat(strings.TrimSuffix(sess.Path, ".jsonl") + "-agents"); err != nil {
		t.Errorf("child transcript dir missing: %v", err)
	}
}

func TestParallelSubagentsAndPermissions(t *testing.T) {
	fp := &funcProvider{}
	fp.respond = func(req llm.Request) llm.Message {
		if isChild(req) {
			if res := toolResults(req); len(res) > 0 {
				return text("done: " + req.Messages[0].Text())
			}
			n := strings.TrimPrefix(req.Messages[0].Text(), "job ")
			return llm.Message{Blocks: []llm.Block{use("c1", "bash", fmt.Sprintf(`{"command":"sleep 1; echo %s > out%s.txt"}`, n, n))}}
		}
		if res := toolResults(req); len(res) > 0 {
			return text("all done")
		}
		return llm.Message{Blocks: []llm.Block{
			use("p1", "task", `{"description":"one","prompt":"job 1","subagent_type":"general-purpose"}`),
			use("p2", "task", `{"description":"two","prompt":"job 2","subagent_type":"general-purpose"}`),
		}}
	}
	a, dir, _ := newParent(t, fp, permission.ModeDefault)

	start := time.Now()
	evs := drain(a.Run(context.Background(), "run both jobs"), true)
	elapsed := time.Since(start)

	perms := map[string]bool{}
	for _, e := range evs {
		if e.Kind == agent.EvPermission {
			perms[e.Agent] = true
		}
	}
	if !perms["general-purpose: one"] || !perms["general-purpose: two"] {
		t.Errorf("child permission prompts should reach the parent's stream, got %v", perms)
	}
	for _, n := range []string{"1", "2"} {
		if _, err := os.Stat(filepath.Join(dir, "out"+n+".txt")); err != nil {
			t.Errorf("job %s did not run: %v", n, err)
		}
	}
	if elapsed > 1900*time.Millisecond {
		t.Errorf("subagents should run in parallel; took %s", elapsed)
	}
	// Both children's edits belong to the parent's turn for /undo.
	// (bash writes aren't checkpointed; this just checks undo doesn't break.)
	_, _ = a.Undo()
}

func TestPlanModeAppliesToChildren(t *testing.T) {
	fp := &funcProvider{}
	fp.respond = func(req llm.Request) llm.Message {
		if isChild(req) {
			if res := toolResults(req); len(res) > 0 {
				return text("child saw: " + res[0].Content)
			}
			return llm.Message{Blocks: []llm.Block{use("c1", "write", `{"path":"x.txt","content":"x"}`)}}
		}
		if res := toolResults(req); len(res) > 0 {
			return text(res[0].Content)
		}
		return llm.Message{Blocks: []llm.Block{use("p1", "task", `{"description":"write","prompt":"write x","subagent_type":"general-purpose"}`)}}
	}
	a, dir, _ := newParent(t, fp, permission.ModePlan)
	drain(a.Run(context.Background(), "go"), true)
	if _, err := os.Stat(filepath.Join(dir, "x.txt")); !os.IsNotExist(err) {
		t.Fatal("plan mode must stop subagent writes")
	}
	reqs := fp.requests()
	if res := toolResults(reqs[len(reqs)-1]); !strings.Contains(res[0].Content, "plan mode") {
		t.Errorf("task result = %+v", res)
	}
}

func TestDefinitions(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) {
		os.MkdirAll(root, 0o755)
		os.WriteFile(filepath.Join(root, name), []byte(content), 0o644)
	}
	write("reviewer.md", "---\nname: reviewer\ndescription: Reviews diffs for bugs\ntools: Read, Grep, Glob, mcp__github\nmodel: sonnet\n---\nYou review code.\n")
	write("explore.md", "---\ndescription: My explore override\ntools: [read]\n---\nCustom explore.\n")
	write("nodesc.md", "---\nname: nodesc\n---\nx\n")
	write("notes.txt", "ignored")
	set := Discover([]string{root})

	r, ok := set.Get("reviewer")
	if !ok || r.Model != "sonnet" || r.Prompt != "You review code." || strings.Join(r.Tools, "|") != "Read|Grep|Glob|mcp__github" {
		t.Fatalf("reviewer = %+v", r)
	}
	for tool, want := range map[string]bool{"read": true, "grep": true, "bash": false, "mcp__github__get_issue": true, "mcp__githubx__a": false} {
		if got := r.toolAllowed(tool); got != want {
			t.Errorf("toolAllowed(%s)=%v", tool, got)
		}
	}
	editor := Definition{Tools: []string{"Read", "Edit"}}
	claude := Definition{Tools: []string{"MultiEdit"}}
	if !editor.toolAllowed("multi_edit") || !claude.toolAllowed("multi_edit") || claude.toolAllowed("edit") {
		t.Error("Edit should allow multi_edit, and MultiEdit should name it")
	}
	if e, _ := set.Get("explore"); e.Description != "My explore override" || strings.Join(e.Tools, ",") != "read" {
		t.Errorf("user definition should override builtin: %+v", e)
	}
	if _, ok := set.Get("general-purpose"); !ok {
		t.Error("builtin missing")
	}
	if len(set.Warnings) != 1 || !strings.Contains(set.Warnings[0], "description is required") {
		t.Errorf("warnings = %v", set.Warnings)
	}
}

// namedTool is a do-nothing tool with a given name.
type namedTool string

func (n namedTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: string(n), Schema: json.RawMessage(`{"type":"object"}`)}
}
func (namedTool) ReadOnly() bool { return true }
func (namedTool) Run(context.Context, *tools.Env, json.RawMessage) tools.Result {
	return tools.Result{}
}

func TestChildToolsLeaveOutBrowserUnlessNamed(t *testing.T) {
	parent := tools.NewRegistry(namedTool("read"), namedTool("browser_navigate"), namedTool("browser_click"))
	names := func(def Definition) string {
		var out []string
		for _, s := range childTools(parent, def, false).Specs() {
			out = append(out, s.Name)
		}
		slices.Sort(out)
		return strings.Join(out, ",")
	}
	for _, c := range []struct {
		tools []string
		want  string
	}{
		{nil, "read"},
		{[]string{"*"}, "read"},
		{[]string{"read", "browser"}, "browser_click,browser_navigate,read"},
		{[]string{"browser_navigate"}, "browser_navigate"},
	} {
		if got := names(Definition{Tools: c.tools}); got != c.want {
			t.Errorf("tools %v: got %s, want %s", c.tools, got, c.want)
		}
	}
}
