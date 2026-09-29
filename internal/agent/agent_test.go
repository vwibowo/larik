package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"larik/internal/checkpoint"
	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/session"
	"larik/internal/tools"
)

// fakeProvider replays scripted assistant messages and records requests.
type fakeProvider struct {
	script   []llm.Message
	requests []llm.Request
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Stream(ctx context.Context, req llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		f.requests = append(f.requests, req)
		msg := f.script[0]
		f.script = f.script[1:]
		if t := msg.Text(); t != "" && !yield(llm.StreamEvent{Type: llm.EventTextDelta, Text: t}, nil) {
			return
		}
		stop := llm.StopEnd
		if len(msg.ToolUses()) > 0 {
			stop = llm.StopToolUse
		}
		yield(llm.StreamEvent{Type: llm.EventDone, Message: msg, StopReason: stop, Usage: llm.Usage{Input: 100, Output: 10}}, nil)
	}
}

func toolUse(id, name, input string) llm.Block {
	return llm.Block{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}
}

func assistant(blocks ...llm.Block) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Blocks: blocks, Model: "m"}
}

func setup(t *testing.T, mode permission.Mode, script ...llm.Message) (*Agent, *fakeProvider, string) {
	t.Helper()
	dir := t.TempDir()
	fp := &fakeProvider{script: script}
	sess, err := session.Create(filepath.Join(dir, "sessions"), session.Meta{Cwd: dir, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	a := New(Options{
		Provider:    fp,
		Model:       "m",
		Cwd:         dir,
		Tools:       tools.Default(),
		Perms:       permission.NewChecker(mode, permission.Rules{}, dir),
		Session:     sess,
		Checkpoints: checkpoint.New(filepath.Join(dir, "ckpt"), dir),
	})
	return a, fp, dir
}

// drain collects events, answering permission prompts with answer.
func drain(ch <-chan Event, answer PermissionReply) []Event {
	var evs []Event
	for e := range ch {
		if e.Kind == EvPermission {
			e.Reply <- answer
		}
		evs = append(evs, e)
	}
	return evs
}

func kinds(evs []Event) []EventKind {
	var out []EventKind
	for _, e := range evs {
		if e.Kind != EvTextDelta && e.Kind != EvUsage {
			out = append(out, e.Kind)
		}
	}
	return out
}

func TestWriteThenUndo(t *testing.T) {
	a, fp, dir := setup(t, permission.ModeDefault,
		assistant(toolUse("t1", "write", `{"path":"hello.txt","content":"hi\n"}`)),
		assistant(llm.TextBlock("done")),
	)
	evs := drain(a.Run(context.Background(), "create hello.txt"), PermissionReply{Allow: true})

	got := kinds(evs)
	want := []EventKind{EvAssistant, EvPermission, EvToolStart, EvToolEnd, EvAssistant, EvDone}
	if strings.Join(toStrings(got), ",") != strings.Join(toStrings(want), ",") {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "hello.txt")); string(data) != "hi\n" {
		t.Fatalf("file content %q", data)
	}
	// Second request must carry the tool result in a user message.
	last := fp.requests[1].Messages
	res := last[len(last)-1].Blocks[0]
	if res.Type != llm.BlockToolResult || res.ID != "t1" || res.IsError {
		t.Fatalf("unexpected tool result %+v", res)
	}

	if _, err := a.Undo(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hello.txt")); !os.IsNotExist(err) {
		t.Fatalf("undo should delete created file, stat err=%v", err)
	}
}

func TestAlwaysAllowSaveFailureIsReportedAndSessionRuleRemains(t *testing.T) {
	a, _, dir := setup(t, permission.ModeDefault,
		assistant(toolUse("t1", "write", `{"path":"first.txt","content":"first"}`)),
		assistant(llm.TextBlock("done")),
		assistant(toolUse("t2", "write", `{"path":"second.txt","content":"second"}`)),
		assistant(llm.TextBlock("done")),
	)
	a.opts.OnAllowRule = func(string) error { return fmt.Errorf("disk unavailable") }
	result := make(chan error, 1)
	evs := drain(a.Run(context.Background(), "first"), PermissionReply{Allow: true, Always: true, Persisted: result})
	if err := <-result; err == nil || !strings.Contains(err.Error(), "disk unavailable") {
		t.Fatalf("persistence result = %v", err)
	}
	var warned bool
	for _, ev := range evs {
		if ev.Kind == EvNotice && strings.Contains(ev.Text, "disk unavailable") && strings.Contains(ev.Text, "only to this session") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("save failure was not reported: %v", kinds(evs))
	}
	for _, ev := range drain(a.Run(context.Background(), "second"), PermissionReply{Allow: false}) {
		if ev.Kind == EvPermission {
			t.Fatal("session rule did not suppress a second prompt")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "second.txt")); err != nil {
		t.Fatalf("second permitted write did not run: %v", err)
	}
}

func TestDeniedPermissionReturnsError(t *testing.T) {
	a, fp, dir := setup(t, permission.ModeDefault,
		assistant(toolUse("t1", "bash", `{"command":"touch x"}`)),
		assistant(llm.TextBlock("ok")),
	)
	drain(a.Run(context.Background(), "go"), PermissionReply{Allow: false, Reason: "not now"})
	if _, err := os.Stat(filepath.Join(dir, "x")); !os.IsNotExist(err) {
		t.Fatal("denied command ran")
	}
	msgs := fp.requests[1].Messages
	res := msgs[len(msgs)-1].Blocks[0]
	if !res.IsError || !strings.Contains(res.Content, "not now") {
		t.Fatalf("expected denial with feedback, got %+v", res)
	}
}

func TestPlanModeAndInvalidJSON(t *testing.T) {
	bad := toolUse("t2", "read", "")
	bad.Input = nil
	a, fp, _ := setup(t, permission.ModePlan,
		assistant(toolUse("t1", "edit", `{"path":"a","old_string":"x","new_string":"y"}`), bad),
		assistant(llm.TextBlock("ok")),
	)
	evs := drain(a.Run(context.Background(), "go"), PermissionReply{})
	for _, e := range evs {
		if e.Kind == EvPermission {
			t.Fatal("plan mode should deny without asking")
		}
	}
	msgs := fp.requests[1].Messages
	blocks := msgs[len(msgs)-1].Blocks
	if len(blocks) != 2 || !strings.Contains(blocks[0].Content, "plan mode") || !strings.Contains(blocks[1].Content, "INVALID_JSON") {
		t.Fatalf("unexpected results %+v", blocks)
	}
}

func TestSessionResume(t *testing.T) {
	a, _, _ := setup(t, permission.ModeYolo, assistant(llm.TextBlock("hello back")))
	drain(a.Run(context.Background(), "hello"), PermissionReply{})
	path := a.opts.Session.Path
	a.opts.Session.Close()

	s, st, err := session.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(st.Messages) != 2 || st.Messages[1].Text() != "hello back" || st.Usage.Input != 100 {
		t.Fatalf("bad restored state: %+v", st)
	}
}

func TestCompaction(t *testing.T) {
	a, fp, _ := setup(t, permission.ModeYolo,
		assistant(llm.TextBlock("first answer")),
		assistant(llm.TextBlock("<summary>user said hello</summary>")),
		assistant(llm.TextBlock("second answer")),
	)
	drain(a.Run(context.Background(), "hello"), PermissionReply{})
	if _, err := a.Compact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	drain(a.Run(context.Background(), "next"), PermissionReply{})
	msgs := fp.requests[2].Messages
	// Summary and the new prompt merge into one user turn.
	if len(msgs) != 1 || len(msgs[0].Blocks) != 2 || !strings.Contains(msgs[0].Blocks[0].Text, "user said hello") || msgs[0].Blocks[1].Text != "next" {
		t.Fatalf("after compaction got %d messages: %+v", len(msgs), msgs)
	}
	// The summary points at the transcript so details can be looked up.
	if !strings.Contains(msgs[0].Blocks[0].Text, a.SessionPath()) {
		t.Fatalf("summary should name the transcript %s: %q", a.SessionPath(), msgs[0].Blocks[0].Text)
	}
}

func toStrings(ks []EventKind) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = string(k)
	}
	return out
}

// slowTool is not read-only but is concurrency-safe.
type slowTool struct{}

func (slowTool) ReadOnly() bool        { return false }
func (slowTool) ConcurrencySafe() bool { return true }
func (slowTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: "slow", Description: "d", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (slowTool) Run(ctx context.Context, _ *tools.Env, _ json.RawMessage) tools.Result {
	time.Sleep(300 * time.Millisecond)
	return tools.Result{Content: "ok"}
}

func TestConcurrencySafeToolsRunInParallel(t *testing.T) {
	fp := &fakeProvider{script: []llm.Message{
		assistant(toolUse("a", "slow", `{}`), toolUse("b", "slow", `{}`), toolUse("c", "slow", `{}`)),
		assistant(llm.TextBlock("done")),
	}}
	a := New(Options{Provider: fp, Model: "m", Cwd: t.TempDir(), Tools: tools.NewRegistry(slowTool{}),
		Perms: permission.NewChecker(permission.ModeYolo, permission.Rules{}, t.TempDir())})
	start := time.Now()
	drain(a.Run(context.Background(), "go"), PermissionReply{})
	if d := time.Since(start); d > 700*time.Millisecond {
		t.Errorf("3 concurrency-safe calls took %s; want parallel", d)
	}
}

// thinkingProvider thinks for a moment before answering.
type thinkingProvider struct{}

func (thinkingProvider) Name() string { return "fake" }
func (thinkingProvider) Stream(ctx context.Context, req llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		if !yield(llm.StreamEvent{Type: llm.EventThinkingDelta, Text: "hmm"}, nil) {
			return
		}
		time.Sleep(30 * time.Millisecond)
		if !yield(llm.StreamEvent{Type: llm.EventTextDelta, Text: "hi"}, nil) {
			return
		}
		msg := assistant(llm.Block{Type: llm.BlockThinking, Text: "hmm"}, llm.TextBlock("hi"))
		yield(llm.StreamEvent{Type: llm.EventDone, Message: msg, StopReason: llm.StopEnd}, nil)
	}
}

func TestThinkingTimeIsSaved(t *testing.T) {
	a, _, _ := setup(t, permission.ModeYolo)
	a.opts.Provider = thinkingProvider{}
	drain(a.Run(context.Background(), "hello"), PermissionReply{})
	path := a.opts.Session.Path
	a.opts.Session.Close()

	s, st, err := session.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	think := st.Messages[1].Blocks[0]
	if think.Type != llm.BlockThinking || think.DurationMS < 30 || think.DurationMS > 5000 {
		t.Fatalf("resumed thinking block should carry how long it took: %+v", think)
	}
}

func TestAutoCompactCanBeTurnedOff(t *testing.T) {
	a, _, _ := setup(t, permission.ModeYolo)
	a.messages = []llm.Message{llm.UserText("a"), assistant(llm.TextBlock("b")), llm.UserText("c"), assistant(llm.TextBlock("d"))}
	a.lastContext = 1 << 30
	if !a.needsCompaction() {
		t.Fatal("a full context should compact by default")
	}
	a.SetAutoCompact(false)
	if a.needsCompaction() {
		t.Fatal("auto-compact off should never compact on its own")
	}
}

func TestLanguageAppliesOnFreshContext(t *testing.T) {
	a := New(Options{System: "base", Language: "Indonesian"})
	if !strings.Contains(a.opts.System, "Respond in Indonesian") {
		t.Fatalf("language should be in the system prompt: %q", a.opts.System)
	}
	a.SetLanguage("")
	if !strings.Contains(a.opts.System, "Indonesian") {
		t.Fatal("the prompt should stay stable until the context is cleared")
	}
	a.Clear()
	if a.opts.System != "base" {
		t.Fatalf("clearing should apply the new language: %q", a.opts.System)
	}
}

func TestClearRebuildsSystemPrompt(t *testing.T) {
	version := "v1"
	a := New(Options{System: "base v1", BuildSystem: func() string { return "base " + version }, Language: "Indonesian"})
	version = "v2"
	if a.opts.System != WithLanguage("base v1", "Indonesian") {
		t.Fatalf("the prompt should stay fixed within a context: %q", a.opts.System)
	}
	a.Clear()
	if a.opts.System != WithLanguage("base v2", "Indonesian") {
		t.Fatalf("clearing should rebuild the prompt and keep the language: %q", a.opts.System)
	}
}

// cancelOnDone cancels the turn as the model's reply completes, the
// moment between saving a reply and running its tools.
type cancelOnDone struct {
	*fakeProvider
	cancel context.CancelFunc
}

func (c cancelOnDone) Stream(ctx context.Context, req llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		for ev, err := range c.fakeProvider.Stream(ctx, req) {
			if ev.Type == llm.EventDone {
				c.cancel()
			}
			if !yield(ev, err) {
				return
			}
		}
	}
}

// unanswered returns tool_use ids in msgs without a following tool_result.
func unanswered(msgs []llm.Message) []string {
	var missing []string
	for i, m := range msgs {
		if m.Role != llm.RoleAssistant {
			continue
		}
		answered := map[string]bool{}
		if i+1 < len(msgs) {
			for _, b := range msgs[i+1].Blocks {
				if b.Type == llm.BlockToolResult {
					answered[b.ID] = true
				}
			}
		}
		for _, u := range m.ToolUses() {
			if !answered[u.ID] {
				missing = append(missing, u.ID)
			}
		}
	}
	return missing
}

func TestInterruptAfterReplyAnswersToolCalls(t *testing.T) {
	a, fp, _ := setup(t, permission.ModeYolo, assistant(toolUse("t1", "read", `{"path":"x"}`)))
	ctx, cancel := context.WithCancel(context.Background())
	a.opts.Provider = cancelOnDone{fp, cancel}
	evs := drain(a.Run(ctx, "go"), PermissionReply{Allow: true})
	if last := evs[len(evs)-1]; last.StopReason != "interrupted" {
		t.Fatalf("stop = %q, want interrupted", last.StopReason)
	}
	if m := unanswered(a.messages); len(m) > 0 {
		t.Fatalf("unanswered tool calls in context: %v", m)
	}
	st, err := session.Load(a.SessionPath())
	if err != nil {
		t.Fatal(err)
	}
	if m := unanswered(st.Messages); len(m) > 0 {
		t.Fatalf("unanswered tool calls in the session file: %v", m)
	}
}

func TestDanglingToolUseAnsweredOnNextPrompt(t *testing.T) {
	a, fp, _ := setup(t, permission.ModeYolo, assistant(llm.TextBlock("ok")))
	a.Restore(&session.State{Messages: []llm.Message{
		llm.UserText("first"),
		assistant(toolUse("t1", "read", `{"path":"x"}`)),
	}})
	drain(a.Run(context.Background(), "again"), PermissionReply{Allow: true})
	if m := unanswered(fp.requests[0].Messages); len(m) > 0 {
		t.Fatalf("request sent with unanswered tool calls: %v", m)
	}
}

func TestCompactionKeepsOpenTodos(t *testing.T) {
	a, _, _ := setup(t, permission.ModeYolo,
		assistant(toolUse("t1", "todo_write", `{"todos":[{"content":"Read the code","status":"completed"},{"content":"Fix the bug","status":"in_progress"}]}`)),
		assistant(llm.TextBlock("on it")),
		assistant(llm.TextBlock("<summary>fixing a bug</summary>")),
	)
	drain(a.Run(context.Background(), "fix it"), PermissionReply{})
	summary, err := a.Compact(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "[x] Read the code") || !strings.Contains(summary, "[>] Fix the bug") {
		t.Fatalf("the open task list should survive compaction: %q", summary)
	}
}

func TestTranscriptWriteFailureIsReportedOnce(t *testing.T) {
	a, _, _ := setup(t, permission.ModeYolo, assistant(llm.TextBlock("one")), assistant(llm.TextBlock("two")))
	a.opts.Session.Close() // every later write fails
	count := func(evs []Event) int {
		n := 0
		for _, e := range evs {
			if e.Kind == EvNotice && strings.Contains(e.Text, "couldn't save to the session transcript") {
				n++
			}
		}
		return n
	}
	first := drain(a.Run(context.Background(), "hi"), PermissionReply{})
	second := drain(a.Run(context.Background(), "again"), PermissionReply{})
	if count(first) != 1 || count(second) != 0 {
		t.Fatalf("reports: first turn %d, second %d", count(first), count(second))
	}
	if last := first[len(first)-1]; last.Kind != EvDone {
		t.Fatalf("the turn should still finish: %+v", last)
	}
}
