package agent

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/session"
	"larik/internal/tools"
)

// catalogModel adds a model to the catalog for one test, so a test can
// pick a context window and output limit instead of the 128k default.
func catalogModel(t *testing.T, id string, info llm.ModelInfo) {
	t.Helper()
	info.ID = id
	old, existed := llm.Catalog[id]
	llm.Catalog[id] = info
	t.Cleanup(func() {
		if existed {
			llm.Catalog[id] = old
		} else {
			delete(llm.Catalog, id)
		}
	})
}

// isCompaction reports whether a request is asking for a summary rather
// than continuing the conversation.
func isCompaction(req llm.Request) bool {
	if len(req.Messages) == 0 {
		return false
	}
	return strings.Contains(req.Messages[len(req.Messages)-1].Text(), "Summarize the transcript inside")
}

// step is one scripted outcome: a reply, or the error to fail with.
type step struct {
	msg llm.Message
	err error
}

// dualProvider answers conversation requests and compaction requests from
// separate scripts, so a test can fail one without touching the other. It
// reports the same usage for every request.
type dualProvider struct {
	turns    []step
	compacts []step
	usage    llm.Usage
	requests []llm.Request
}

func (p *dualProvider) Name() string { return "dual" }

func (p *dualProvider) Stream(ctx context.Context, req llm.Request) iter.Seq2[llm.StreamEvent, error] {
	p.requests = append(p.requests, req)
	queue := &p.turns
	if isCompaction(req) {
		queue = &p.compacts
	}
	return func(yield func(llm.StreamEvent, error) bool) {
		if len(*queue) == 0 {
			yield(llm.StreamEvent{}, errors.New("no scripted reply left"))
			return
		}
		s := (*queue)[0]
		*queue = (*queue)[1:]
		if s.err != nil {
			yield(llm.StreamEvent{}, s.err)
			return
		}
		stop := llm.StopEnd
		if len(s.msg.ToolUses()) > 0 {
			stop = llm.StopToolUse
		}
		yield(llm.StreamEvent{Type: llm.EventDone, Message: s.msg, StopReason: stop, Usage: p.usage}, nil)
	}
}

// A compaction that fails at the threshold is reported and the turn goes
// on. What must not happen is the overflow recovery being disarmed by it:
// it is the only thing left that can get an oversized request through.
func TestAFailedCompactionLeavesOverflowRecoveryArmed(t *testing.T) {
	p := &dualProvider{
		// Every reply reports a prompt filling most of the window, so the
		// loop wants to compact before each request.
		usage: llm.Usage{Input: 500_000, Output: 10},
		turns: []step{
			{msg: assistant(toolUse("t1", "glob", `{"pattern":"*.none"}`))},
			{msg: assistant(toolUse("t2", "glob", `{"pattern":"*.none"}`))},
			{err: llm.ErrContextOverflow},
			{msg: assistant(llm.TextBlock("done"))},
		},
		compacts: []step{
			{err: errors.New("summarizer unavailable")},
			{msg: assistant(llm.TextBlock("<summary>recovered</summary>"))},
		},
	}
	dir := t.TempDir()
	a := New(Options{Provider: p, Model: "m", Cwd: dir, Tools: tools.Default(), MaxTurns: 10,
		Perms: permission.NewChecker(permission.ModeYolo, permission.Rules{}, dir)})

	evs := drain(a.Run(context.Background(), "go"), PermissionReply{})

	var stop, notice string
	compacted := 0
	for _, e := range evs {
		switch {
		case e.Kind == EvDone:
			stop = e.StopReason
		case e.Kind == EvNotice && strings.Contains(e.Text, "auto-compaction failed"):
			notice = e.Text
		case e.Kind == EvCompacted:
			compacted++
		}
	}
	if stop != string(llm.StopEnd) {
		t.Fatalf("stop = %q, want the turn to recover and finish", stop)
	}
	if notice == "" {
		t.Error("the failed compaction should be reported")
	}
	if compacted != 1 {
		t.Errorf("compactions = %d, want the overflow one to have run", compacted)
	}
	if len(p.compacts) != 0 {
		t.Errorf("%d compaction replies unused: the overflow never compacted", len(p.compacts))
	}
}

// lastContext only knows what the provider measured for the previous
// request, and a resumed session has no measurement at all. The context
// has to be sized before the request, or the first thing a resumed full
// session does is overflow.
func TestAResumedFullContextCompactsBeforeTheFirstRequest(t *testing.T) {
	a, fp, _ := setup(t, permission.ModeYolo,
		assistant(llm.TextBlock("<summary>earlier work</summary>")),
		assistant(llm.TextBlock("answer")),
	)
	big := strings.Repeat("x", 200_000) // ~50k tokens each, 200k in all
	a.Restore(&session.State{Messages: []llm.Message{
		llm.UserText(big), assistant(llm.TextBlock(big)),
		llm.UserText(big), assistant(llm.TextBlock(big)),
	}})
	if a.lastContext != 0 {
		t.Fatalf("a resumed session has no measurement yet, got %d", a.lastContext)
	}

	evs := drain(a.Run(context.Background(), "next"), PermissionReply{})

	if len(fp.requests) != 2 {
		t.Fatalf("got %d requests, want a compaction and then the prompt", len(fp.requests))
	}
	if !isCompaction(fp.requests[0]) {
		t.Error("the first request should be the compaction")
	}
	msgs := fp.requests[1].Messages
	if len(msgs) != 1 || !strings.Contains(msgs[0].Text(), "earlier work") {
		t.Fatalf("the prompt should go out in the compacted context: %+v", msgs)
	}
	for _, e := range evs {
		if e.Kind == EvCompacted && e.Compaction != nil && e.Compaction.Trigger != "auto" {
			t.Errorf("trigger = %q, want auto", e.Compaction.Trigger)
		}
	}
}

// A large prompt belongs to the request being sized even though it must not
// be appended until after the old conversation is compacted. Otherwise a
// nearly full context overflows once before the estimator gets a vote.
func TestALargeNewPromptTriggersCompactionBeforeItsRequest(t *testing.T) {
	a, fp, _ := setup(t, permission.ModeYolo,
		assistant(llm.TextBlock("<summary>earlier work</summary>")),
		assistant(llm.TextBlock("answer")),
	)
	// A single prior exchange is enough: the new prompt, not the number of
	// turns, is what makes compaction necessary.
	a.messages = []llm.Message{llm.UserText("one"), assistant(llm.TextBlock("two"))}
	prompt := strings.Repeat("x", 500_000) // pushes the 128k default over 80%

	drain(a.Run(context.Background(), prompt), PermissionReply{})

	if len(fp.requests) != 2 || !isCompaction(fp.requests[0]) {
		t.Fatalf("requests = %d, first compaction = %v; want compaction before prompt", len(fp.requests), len(fp.requests) > 0 && isCompaction(fp.requests[0]))
	}
	last := fp.requests[1].Messages
	if len(last) != 1 || !strings.Contains(last[0].Text(), "earlier work") || !strings.Contains(last[0].Text(), prompt) {
		t.Fatalf("request after compaction should contain summary then the prompt: messages=%d summary=%v prompt=%v", len(last), len(last) == 1 && strings.Contains(last[0].Text(), "earlier work"), len(last) == 1 && strings.Contains(last[0].Text(), prompt))
	}
}

// Compaction replaces the conversation but keeps the system prompt and tools.
// A prefix that fills the whole window cannot benefit, but one between the 80%
// trigger and the actual window can still make room by dropping old messages.
func TestCompactionDistinguishesALargePrefixFromAnImpossibleOne(t *testing.T) {
	catalogModel(t, "cramped", llm.ModelInfo{ContextWindow: 10_000, MaxOutput: 1_000})
	a, _, _ := setup(t, permission.ModeYolo)
	a.opts.Model, a.opts.Tools = "cramped", tools.NewRegistry()
	a.messages = []llm.Message{llm.UserText("a"), assistant(llm.TextBlock("b")), llm.UserText("c"), assistant(llm.TextBlock("d"))}
	a.lastContext = 9_500

	a.opts.System = strings.Repeat("s", 34_000) // ~8.5k tokens: high, but room can still be freed
	if !a.needsCompaction() {
		t.Error("a prefix below the window can still benefit from dropping old messages")
	}
	a.opts.System = strings.Repeat("s", 44_000) // ~11k tokens in a 10k window
	if a.needsCompaction() {
		t.Error("a prefix larger than the window should not compact: it cannot help")
	}
}

func TestCompactionThresholdBoundary(t *testing.T) {
	catalogModel(t, "ten-k", llm.ModelInfo{ContextWindow: 10_000, MaxOutput: 1_000})
	a, _, _ := setup(t, permission.ModeYolo)
	a.opts.Model, a.opts.Tools, a.opts.System = "ten-k", tools.NewRegistry(), ""
	a.messages = []llm.Message{llm.UserText("a"), assistant(llm.TextBlock("b")), llm.UserText("c"), assistant(llm.TextBlock("d"))}

	a.lastContext = 8_000 // exactly the threshold
	if a.needsCompaction() {
		t.Error("exactly at the threshold should not compact")
	}
	a.lastContext = 8_001
	if !a.needsCompaction() {
		t.Error("just over the threshold should compact")
	}
}

// A summary has to fit in the model writing it, and be a small enough
// share of the window it frees to be worth writing.
func TestCompactionOutputAllowance(t *testing.T) {
	for _, tc := range []struct {
		name              string
		window, maxOutput int
		want              int
	}{
		{"a roomy model keeps the default", 1_000_000, 128_000, compactMaxOutput},
		{"the model's own limit wins", 200_000, 7, 7},
		{"a share of a small window", 20_000, 64_000, 5_000},
		{"never below the floor", 2_000, 64_000, compactMinOutput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalogModel(t, "sized", llm.ModelInfo{ContextWindow: tc.window, MaxOutput: tc.maxOutput})
			a, fp, _ := setup(t, permission.ModeYolo, assistant(llm.TextBlock("<summary>s</summary>")))
			a.opts.Model = "sized"
			a.messages = []llm.Message{llm.UserText("something to summarize")}
			if _, _, err := a.Compact(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			if got := fp.requests[0].MaxTokens; got != tc.want {
				t.Errorf("MaxTokens = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestExtractSummary(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"tagged", "Here you go:\n<summary>the work</summary>\nAnything else?", "the work"},
		{"untagged replies are kept whole", "the work", "the work"},
		{"an unclosed tag keeps the rest", "ok:\n<summary>the work", "the work"},
		{"the first complete pair wins", "<summary>first</summary> and <summary>second</summary>", "first"},
		{"a repeated opening tag is dropped", "<summary>\n<summary>the work</summary>", "the work"},
		{"whitespace only is empty", "<summary>\n\n</summary>", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractSummary(tc.in); got != tc.want {
				t.Errorf("extractSummary(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// An image costs about the same whatever its byte size, so its base64
// payload must not be measured as if it were text: one screenshot would
// otherwise look like a full context.
func TestEstimateDoesNotCountImagesAsText(t *testing.T) {
	shot := llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{
		{Type: llm.BlockImage, MediaType: "image/png", Data: strings.Repeat("A", 1_000_000)},
	}}
	if got := estimateMessages([]llm.Message{shot}); got != imageTokens {
		t.Errorf("one image estimated at %d tokens, want %d", got, imageTokens)
	}
	text := llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{llm.TextBlock(strings.Repeat("x", 4_000))}}
	if got := estimateMessages([]llm.Message{text}); got != 1_000 {
		t.Errorf("4000 characters estimated at %d tokens, want 1000", got)
	}
}
