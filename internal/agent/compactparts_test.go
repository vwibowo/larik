package agent

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/session"
	"larik/internal/tools"
)

// limitedProvider has a real context window: a request whose prompt and
// output allowance exceed limit is rejected with ErrContextOverflow, as a
// provider would. It counts three characters a token, stricter than the
// agent's estimate (four), so the estimate erring low is exercised too.
// Summary requests get a summary; others are answered from turns.
type limitedProvider struct {
	limit    int
	perToken int // characters a token; 3 when unset
	turns    []llm.Message
	requests []llm.Request // every request, rejected ones included
	rejected []llm.Request
	usage    llm.Usage
}

func (p *limitedProvider) Name() string { return "limited" }

func requestTokens(req llm.Request) int { return countTokens(req, 3) }

func countTokens(req llm.Request, perToken int) int {
	chars := len(req.System)
	for _, t := range req.Tools {
		chars += len(t.Name) + len(t.Description) + len(t.Schema)
	}
	for _, m := range req.Messages {
		for _, b := range m.Blocks {
			chars += len(b.Text) + len(b.Content) + len(b.Input) + len(b.Name) + len(b.ID)
		}
	}
	return chars/perToken + req.MaxTokens
}

func requestText(req llm.Request) string {
	var b strings.Builder
	for _, m := range req.Messages {
		for _, bl := range m.Blocks {
			b.WriteString(bl.Text)
			b.WriteString(bl.Content)
		}
	}
	return b.String()
}

func (p *limitedProvider) Stream(ctx context.Context, req llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		p.requests = append(p.requests, req)
		perToken := p.perToken
		if perToken == 0 {
			perToken = 3
		}
		if n := countTokens(req, perToken); n > p.limit {
			p.rejected = append(p.rejected, req)
			yield(llm.StreamEvent{}, fmt.Errorf("prompt is too long: %d tokens > %d: %w", n, p.limit, llm.ErrContextOverflow))
			return
		}
		var msg llm.Message
		switch {
		case isCompaction(req):
			msg = assistant(llm.TextBlock(fmt.Sprintf("<summary>summary after request %d</summary>", len(p.requests))))
		case len(p.turns) > 0:
			msg, p.turns = p.turns[0], p.turns[1:]
		default:
			yield(llm.StreamEvent{}, fmt.Errorf("no scripted reply left"))
			return
		}
		yield(llm.StreamEvent{Type: llm.EventDone, Message: msg, StopReason: llm.StopEnd, Usage: p.usage}, nil)
	}
}

// checkPaired fails if a request holds a tool call without its result
// right after it, which every provider rejects.
func checkPaired(t *testing.T, req llm.Request) {
	t.Helper()
	for i, m := range req.Messages {
		uses := m.ToolUses()
		if len(uses) == 0 {
			continue
		}
		if i+1 >= len(req.Messages) {
			t.Fatalf("message %d: tool call with no result after it", i)
		}
		answered := map[string]bool{}
		for _, b := range req.Messages[i+1].Blocks {
			if b.Type == llm.BlockToolResult {
				answered[b.ID] = true
			}
		}
		for _, u := range uses {
			if !answered[u.ID] {
				t.Fatalf("message %d: tool call %s unanswered in the next message", i, u.ID)
			}
		}
	}
}

// bigTranscript is a long working session: prompts, tool calls and their
// results, and one result far larger than any summary request.
func bigTranscript(steps int, latest string) []llm.Message {
	var msgs []llm.Message
	for i := range steps {
		msgs = append(msgs,
			llm.UserText(fmt.Sprintf("step %d: %s", i, strings.Repeat("u", 8_000))),
			assistant(llm.TextBlock("looking"), toolUse(fmt.Sprintf("call-%d", i), "glob", `{"pattern":"*.go"}`)),
			llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ID: fmt.Sprintf("call-%d", i), Name: "glob", Content: strings.Repeat("r", 16_000)}}},
			assistant(llm.TextBlock(fmt.Sprintf("done with step %d", i))),
		)
	}
	msgs = append(msgs,
		llm.UserText("dump the log"),
		assistant(toolUse("huge", "bash", `{"command":"cat log"}`)),
		llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ID: "huge", Name: "bash", Content: strings.Repeat("L", 100_000)}}},
		assistant(llm.TextBlock("that is a big log")),
		llm.UserText(latest),
	)
	return msgs
}

func smallWindowAgent(t *testing.T, p llm.Provider) *Agent {
	t.Helper()
	catalogModel(t, "small", llm.ModelInfo{ContextWindow: 20_000, MaxOutput: 4_000})
	a, _, _ := setup(t, permission.ModeYolo)
	a.opts.Provider, a.opts.Model, a.opts.Tools = p, "small", tools.Default()
	return a
}

// A transcript many times the summarizer's window cannot be summarized in
// one request. It is summarized in parts, each of which the provider
// accepts, without breaking tool pairing, keeping the latest request, and
// telling the user what was lost. The saved transcript keeps everything.
func TestCompactingAnOversizedTranscriptSummarizesItInBoundedParts(t *testing.T) {
	p := &limitedProvider{limit: 20_000, usage: llm.Usage{Input: 50, Output: 5}}
	a := smallWindowAgent(t, p)
	const latest = "LATEST-TASK: keep the parser streaming and never buffer the whole file"
	msgs := bigTranscript(12, latest)
	for _, m := range msgs {
		a.appendMessage(m, nil)
	}

	var notices []string
	summary, info, err := a.Compact(context.Background(), func(e Event) {
		if e.Kind == EvNotice {
			notices = append(notices, e.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.rejected) != 0 {
		t.Fatalf("%d of %d summary requests were too large for the window", len(p.rejected), len(p.requests))
	}
	if n := len(p.requests); n < 2 || n > compactMaxParts {
		t.Fatalf("summary requests = %d, want parts within the cap of %d", n, compactMaxParts)
	}
	for i, req := range p.requests {
		if !isCompaction(req) || len(req.Tools) != 0 {
			t.Errorf("request %d: compaction=%v tools=%d; a part is a summary request without tools", i, isCompaction(req), len(req.Tools))
		}
		checkPaired(t, req)
	}
	if !strings.Contains(requestText(p.requests[len(p.requests)-1]), latest) {
		t.Error("the last part should carry the latest request for the summary to state")
	}
	if !strings.Contains(summary, latest) || !strings.Contains(summary, "summary after request") {
		t.Errorf("summary should be the last part's, with the latest request verbatim: %q", summary)
	}
	if !strings.Contains(summary, "shortened or left out") {
		t.Error("the summary should tell the model that content was lost")
	}
	var lossNoticed bool
	for _, n := range notices {
		if strings.Contains(n, "summarized in") && strings.Contains(n, "shortened or left out") {
			lossNoticed = true
		}
	}
	if !lossNoticed {
		t.Errorf("no context-loss notice: %q", notices)
	}
	if info.Available {
		t.Error("parts measure pieces, not the conversation: no measured savings")
	}

	// Only the compaction entry was added: the transcript is all there.
	st, err := session.Load(a.SessionPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.All) != len(msgs) {
		t.Errorf("saved messages = %d, want the %d originals untouched", len(st.All), len(msgs))
	}
	if len(st.Messages) != 1 || !strings.Contains(st.Messages[0].Text(), latest) {
		t.Errorf("resumed context should be the summary alone")
	}
	if want := 50 * len(p.requests); st.Usage.Input != want || a.Stats().Total.Input != want {
		t.Errorf("usage saved %d, counted %d; want %d, every part once", st.Usage.Input, a.Stats().Total.Input, want)
	}
}

// The estimate errs low, so a transcript it says fits can still be too
// large. The provider's rejection is not the end: the summary is written
// in parts instead.
func TestCompactionFallsBackToPartsWhenTheProviderRejectsTheWholeRequest(t *testing.T) {
	p := &limitedProvider{limit: 20_000, usage: llm.Usage{Input: 50, Output: 5}}
	a := smallWindowAgent(t, p)
	a.opts.Tools = tools.NewRegistry()
	for i := range 6 { // ~60k characters: 15k estimated tokens, 20k as counted
		a.messages = append(a.messages, llm.UserText(strings.Repeat("q", 5_000)), assistant(llm.TextBlock(fmt.Sprintf("answer %d %s", i, strings.Repeat("a", 5_000)))))
	}
	a.messages = append(a.messages, llm.UserText("LATEST-TASK: ship it"))

	summary, _, err := a.Compact(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.rejected) != 1 || len(p.rejected[0].Messages) < 2 {
		t.Fatalf("rejected = %d; want only the whole-transcript request rejected", len(p.rejected))
	}
	if len(p.requests) < 3 {
		t.Errorf("requests = %d; want the rejected one and then parts", len(p.requests))
	}
	if !strings.Contains(summary, "LATEST-TASK: ship it") {
		t.Errorf("summary lost the latest request: %q", summary)
	}
}

// When the conversation request overflows and so would the summary of the
// whole transcript, recovery summarizes in parts and the turn goes on,
// with the request that was just made still in the context.
func TestOverflowRecoverySurvivesATranscriptTooLargeToSummarizeWhole(t *testing.T) {
	p := &limitedProvider{limit: 20_000, usage: llm.Usage{Input: 50, Output: 5},
		turns: []llm.Message{assistant(llm.TextBlock("finished"))}}
	a := smallWindowAgent(t, p)
	a.opts.NoAutoCompact = true // only the overflow recovery compacts
	a.Restore(&session.State{Messages: bigTranscript(6, "an earlier request")})

	const prompt = "LATEST-TASK: write the report to report.md"
	evs := drain(a.Run(context.Background(), prompt), PermissionReply{})

	var stop string
	var compacted []CompactionInfo
	for _, e := range evs {
		switch {
		case e.Kind == EvDone:
			stop = e.StopReason
		case e.Kind == EvCompacted && e.Compaction != nil:
			compacted = append(compacted, *e.Compaction)
		case e.Kind == EvError:
			t.Errorf("error: %s", e.Text)
		}
	}
	if stop != string(llm.StopEnd) {
		t.Fatalf("stop = %q, want the turn to recover", stop)
	}
	if len(compacted) != 1 || compacted[0].Trigger != "overflow" {
		t.Fatalf("compactions = %+v, want one overflow compaction", compacted)
	}
	if len(p.rejected) != 1 || isCompaction(p.rejected[0]) {
		t.Fatalf("rejected %d requests; want only the original conversation request", len(p.rejected))
	}
	retry := p.requests[len(p.requests)-1]
	if isCompaction(retry) || !strings.Contains(requestText(retry), prompt) {
		t.Errorf("the retried request should continue the task with the prompt in its context")
	}
	checkPaired(t, retry)
}

// tokenCapped sets a token budget already spent.
func tokenCapped(a *Agent, used int) {
	a.opts.TokenBudget = func() (int64, float64) { return 100, 0.9 }
	a.mu.Lock()
	a.usage = llm.Usage{Input: used}
	a.mu.Unlock()
}

func TestManualCompactionIsAdmittedByTheBudget(t *testing.T) {
	a, fp, _ := setup(t, permission.ModeYolo, assistant(llm.TextBlock("<summary>s</summary>")))
	a.messages = []llm.Message{llm.UserText("something"), assistant(llm.TextBlock("reply"))}
	tokenCapped(a, 100)

	_, _, err := a.Compact(context.Background(), nil)
	if err == nil || !isBudgetError(err) {
		t.Fatalf("err = %v, want the budget to refuse the summary", err)
	}
	if len(fp.requests) != 0 {
		t.Errorf("requests = %d, want none over budget", len(fp.requests))
	}
}

// An automatic compaction over budget sends nothing, and the turn ends on
// the budget, reported once, not as a failed compaction.
func TestAutoCompactionOverBudgetSendsNothing(t *testing.T) {
	a, fp, _ := setup(t, permission.ModeYolo)
	big := strings.Repeat("x", 200_000)
	a.Restore(&session.State{Messages: []llm.Message{llm.UserText(big), assistant(llm.TextBlock(big)), llm.UserText(big)}})
	tokenCapped(a, 100)

	evs := drain(a.Run(context.Background(), "next"), PermissionReply{})

	var stop string
	errs := 0
	for _, e := range evs {
		switch {
		case e.Kind == EvDone:
			stop = e.StopReason
		case e.Kind == EvError:
			errs++
		case e.Kind == EvNotice && strings.Contains(e.Text, "auto-compaction failed"):
			t.Errorf("a budget refusal is not a compaction failure: %q", e.Text)
		}
	}
	if stop != "budget" || errs != 1 || len(fp.requests) != 0 {
		t.Fatalf("stop %q, errors %d, requests %d; want budget, 1, 0", stop, errs, len(fp.requests))
	}
}

// The summary written to recover from an overflow is spent from the same
// budget, so the retry has to be admitted again.
func TestOverflowRetryIsAdmittedByTheBudget(t *testing.T) {
	p := &dualProvider{
		usage:    llm.Usage{Input: 100, Output: 10},
		turns:    []step{{err: fmt.Errorf("too long: %w", llm.ErrContextOverflow)}, {msg: assistant(llm.TextBlock("done"))}},
		compacts: []step{{msg: assistant(llm.TextBlock("<summary>recovered</summary>"))}},
	}
	dir := t.TempDir()
	a := New(Options{Provider: p, Model: "m", Cwd: dir, Tools: tools.Default(), MaxTurns: 5,
		Perms:       permission.NewChecker(permission.ModeYolo, permission.Rules{}, dir),
		TokenBudget: func() (int64, float64) { return 100, 0.9 }})

	evs := drain(a.Run(context.Background(), "go"), PermissionReply{})

	var stop string
	compacted := 0
	for _, e := range evs {
		switch e.Kind {
		case EvDone:
			stop = e.StopReason
		case EvCompacted:
			compacted++
		}
	}
	if stop != "budget" || compacted != 1 {
		t.Fatalf("stop %q, compactions %d; want the budget to stop the retry after compacting", stop, compacted)
	}
	if len(p.turns) != 1 {
		t.Errorf("the retry was sent over budget")
	}
}

// A part the provider still finds too large (the estimate can be off by
// more than its margin) is cut in half and retried, within a fixed number
// of requests.
func TestARejectedPartIsCutAndRetried(t *testing.T) {
	p := &limitedProvider{limit: 20_000, perToken: 1, usage: llm.Usage{Input: 50, Output: 5}}
	a := smallWindowAgent(t, p)
	a.messages = bigTranscript(12, "LATEST-TASK: keep going")

	summary, _, err := a.Compact(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.rejected) == 0 {
		t.Fatal("no part was rejected: the test does not exercise the retry")
	}
	if n := len(p.requests); n > compactMaxParts+compactMaxRetries {
		t.Errorf("requests = %d, want at most %d", n, compactMaxParts+compactMaxRetries)
	}
	if !strings.Contains(summary, "LATEST-TASK: keep going") {
		t.Errorf("summary lost the latest request: %q", summary)
	}
}

// Parts that keep overflowing (here the provider's real window is far
// below the catalog's) end the compaction with an error rather than an
// endless series of requests.
func TestPartsThatNeverFitFailWithinBounds(t *testing.T) {
	p := &limitedProvider{limit: 4_500, perToken: 1}
	a := smallWindowAgent(t, p)
	a.messages = bigTranscript(12, "latest")

	_, _, err := a.Compact(context.Background(), nil)
	if err == nil || !errors.Is(err, llm.ErrContextOverflow) {
		t.Fatalf("err = %v, want the overflow reported", err)
	}
	if n := len(p.requests); n > compactMaxRetries+1 {
		t.Errorf("requests = %d, want the retries bounded", n)
	}
	if len(a.messages) != len(bigTranscript(12, "latest")) {
		t.Error("a failed compaction must leave the context as it was")
	}
}
