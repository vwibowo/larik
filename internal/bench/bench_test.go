package bench

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"larik/internal/agent"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"larik/internal/llm"
	"larik/internal/tools"
)

// TestTasksAreSelfChecking is the important test here: it proves each
// built-in task's Verify actually distinguishes broken from fixed,
// without involving any model. A task whose checker always passed (or
// always failed) would make the whole benchmark meaningless.
func TestTasksAreSelfChecking(t *testing.T) {
	fixes := map[string]func(dir string) error{
		"fix-off-by-one": func(dir string) error {
			return replace(dir, "calc/calc.go", "for i := 1;", "for i := 0;")
		},
		"implement-validation": func(dir string) error {
			return replace(dir, "validate/validate.go", "return true // TODO: implement", `
	at := strings.IndexByte(s, '@')
	if at <= 0 || at != strings.LastIndexByte(s, '@') || at == len(s)-1 {
		return false
	}
	local, domain := s[:at], s[at+1:]
	return !strings.ContainsAny(local, " \t") && !strings.ContainsAny(domain, " \t")
`)
		},
		"find-undocumented": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "UNDOCUMENTED.txt"), []byte(strings.Join(surveyAnswer(), "\n")+"\n"), 0o644)
		},
		"persistent-script-state": func(dir string) error {
			data, err := json.Marshal(persistentStateAnswer())
			if err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, "STATE_RESULT.json"), append(data, '\n'), 0o644)
		},
		"rename-across-files": func(dir string) error {
			if err := replaceAll(dir, "order/order.go", "OldName", "NewName"); err != nil {
				return err
			}
			return replaceAll(dir, "order/receipt.go", "OldName", "NewName")
		},
	}
	// implement-validation needs "strings" imported once we add real logic.
	patchImports := map[string]string{
		"implement-validation": "validate/validate.go",
	}

	for _, task := range Tasks() {
		if task.compaction != nil {
			continue // scored from model output, not a fixture mutation
		}
		t.Run(task.Name, func(t *testing.T) {
			dir := t.TempDir()
			if err := task.Setup(dir); err != nil {
				t.Fatalf("setup: %v", err)
			}
			if pass, detail := task.Verify(dir); pass {
				t.Fatalf("the unmodified fixture should fail Verify, but it passed: %s", detail)
			}

			if f, ok := patchImports[task.Name]; ok {
				if err := replace(dir, f, "package validate\n", "package validate\n\nimport \"strings\"\n"); err != nil {
					t.Fatal(err)
				}
			}
			fix, ok := fixes[task.Name]
			if !ok {
				t.Fatalf("no known fix registered for task %q; add one so this test stays meaningful", task.Name)
			}
			if err := fix(dir); err != nil {
				t.Fatalf("applying the fix: %v", err)
			}
			if pass, detail := task.Verify(dir); !pass {
				t.Fatalf("the fixed version should pass Verify: %s", detail)
			}
		})
	}
}

func replace(dir, rel, old, new string) error    { return replaceN(dir, rel, old, new, 1) }
func replaceAll(dir, rel, old, new string) error { return replaceN(dir, rel, old, new, -1) }

func replaceN(dir, rel, old, new string, n int) error {
	p := filepath.Join(dir, rel)
	data, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	if !strings.Contains(string(data), old) {
		return &os.PathError{Op: "replace", Path: p, Err: os.ErrInvalid}
	}
	return os.WriteFile(p, []byte(strings.Replace(string(data), old, new, n)), 0o644)
}

// scriptedProvider replays fixed steps regardless of what it's asked,
// standing in for a model that behaves exactly as expected.
type scriptedProvider struct {
	steps    []llm.Message
	requests []llm.Request
}

func (scriptedProvider) Name() string { return "fake" }

func (p *scriptedProvider) Stream(_ context.Context, req llm.Request) iter.Seq2[llm.StreamEvent, error] {
	p.requests = append(p.requests, req)
	return func(yield func(llm.StreamEvent, error) bool) {
		msg := p.steps[0]
		p.steps = p.steps[1:]
		msg.Role, msg.Model = llm.RoleAssistant, req.Model
		stop := llm.StopEnd
		if len(msg.ToolUses()) > 0 {
			stop = llm.StopToolUse
		}
		yield(llm.StreamEvent{Type: llm.EventDone, Message: msg, StopReason: stop, Usage: llm.Usage{Input: 100, Output: 20}}, nil)
	}
}

func toolUse(name, input string) llm.Block {
	return llm.Block{Type: llm.BlockToolUse, ID: "x", Name: name, Input: json.RawMessage(input)}
}

func TestCompactionRetentionScoring(t *testing.T) {
	task := compactionRetentionTask()
	c := task.compaction
	complete := "Northstar ACME-4821 SQLite WAL 4317 internal/relay/buffer.go FlushPending 64 global mutex add crash-recovery test shutdown hook"
	if got, missing := scoreSummary(complete, c.summaryChecks); got != len(c.summaryChecks) || len(missing) != 0 {
		t.Fatalf("complete summary scored %d/%d, missing %v", got, len(c.summaryChecks), missing)
	}
	if got, missing := scoreSummary(strings.Replace(complete, "ACME-4821", "", 1), c.summaryChecks); got != len(c.summaryChecks)-1 || !slices.Equal(missing, []string{"ticket"}) {
		t.Fatalf("omitted ticket scored %d/%d, missing %v", got, len(c.summaryChecks), missing)
	}

	answer, _ := json.Marshal(c.want)
	if pass, detail := verifyRecovery(string(answer), c.want); !pass {
		t.Fatalf("exact recovery should pass: %s", detail)
	}
	natural := c.want
	natural.Storage = "SQLite in WAL mode"
	natural.File = "internal/relay/buffer.go (unverified prior assistant claim)"
	natural.Function = "FlushPending (unverified prior assistant claim)"
	natural.RejectedApproach = "Global mutex; it stalled readers. Sharded queue is accepted."
	natural.RemainingTodo = "One item remains: add crash-recovery test."
	natural.NextAction = "The immediate next action is to wire FlushPending into shutdown hook."
	naturalJSON, _ := json.Marshal(natural)
	if pass, detail := verifyRecovery(string(naturalJSON), c.want); !pass {
		t.Fatalf("natural wording that retains every fact should pass: %s", detail)
	}
	withExtra := strings.TrimSuffix(string(answer), "}") + `,"invented":"detail"}`
	if pass, _ := verifyRecovery(withExtra, c.want); pass {
		t.Fatal("an invented recovery field should fail")
	}
	wrong := c.want
	wrong.IngestPort = 4318
	wrongJSON, _ := json.Marshal(wrong)
	if pass, detail := verifyRecovery(string(wrongJSON), c.want); pass || !strings.Contains(detail, "ingest_port") {
		t.Fatalf("a stale recovered value should fail its field: pass=%v detail=%q", pass, detail)
	}
}

func TestRunCompactionRetention(t *testing.T) {
	task := compactionRetentionTask()
	answer, _ := json.Marshal(task.compaction.want)
	p := &scriptedProvider{steps: []llm.Message{
		{Blocks: []llm.Block{llm.TextBlock("<summary>Northstar ACME-4821 uses SQLite WAL on 4317. Work is in internal/relay/buffer.go at FlushPending with batch limit 64. The global mutex was rejected. Add crash-recovery test, then wire FlushPending into shutdown hook.</summary>")}},
		{Blocks: []llm.Block{llm.TextBlock(string(answer))}},
	}}

	res := RunWith(context.Background(), task, p, "m", Options{Timeout: 30 * time.Second})
	if !res.Pass {
		t.Fatalf("expected retention run to pass: %+v", res)
	}
	if res.Retained != res.RetentionTotal || res.RetentionTotal != 10 {
		t.Errorf("retention = %d/%d, want 10/10", res.Retained, res.RetentionTotal)
	}
	if res.Compactions != 1 || !res.CompactionMeasured || res.CompactionSavedTokens != 80 {
		t.Errorf("compaction metrics = %+v", res)
	}
	if res.Requests != 2 {
		t.Errorf("requests = %d, want compaction + recovery", res.Requests)
	}
	if len(p.requests) != 2 || len(p.requests[1].Tools) != 0 {
		t.Fatalf("recovery request should have no tools: requests=%d tools=%d", len(p.requests), len(p.requests[1].Tools))
	}
	recoveryContext := p.requests[1].Messages
	if len(recoveryContext) != 1 || strings.Contains(recoveryContext[0].Text(), "discovery shard=") || !strings.Contains(recoveryContext[0].Text(), "Northstar") || !strings.Contains(recoveryContext[0].Text(), task.Prompt) {
		t.Fatalf("recovery should see only the summary and question, not the source transcript: messages=%d", len(recoveryContext))
	}
}

func TestCompactionRetentionKeepFailedCannotReadItsTranscript(t *testing.T) {
	task := compactionRetentionTask()
	answer, _ := json.Marshal(task.compaction.want)
	p := &scriptedProvider{steps: []llm.Message{
		{Blocks: []llm.Block{llm.TextBlock("<summary>intentionally omitted the facts</summary>")}},
		{Blocks: []llm.Block{llm.TextBlock(string(answer))}},
	}}
	res := RunWith(context.Background(), task, p, "m", Options{Timeout: 30 * time.Second, KeepFailed: true})
	if res.Pass || res.Kept == "" {
		t.Fatalf("an incomplete summary must fail and be kept: %+v", res)
	}
	t.Cleanup(func() { os.RemoveAll(res.Kept) })
	if len(p.requests) != 2 || len(p.requests[1].Tools) != 0 {
		t.Fatalf("--keep-failed exposed tools to recovery: requests=%d tools=%d", len(p.requests), len(p.requests[1].Tools))
	}
	if !strings.Contains(res.Detail, "summary omitted") {
		t.Errorf("failure should list omitted facts: %q", res.Detail)
	}
}

func TestRunPassesWhenTheAgentFixesIt(t *testing.T) {
	llm.Catalog["fake-model"] = llm.ModelInfo{ID: "fake-model", ContextWindow: 128_000, MaxOutput: 8_000, InputPrice: 3, OutputPrice: 15}
	t.Cleanup(func() { delete(llm.Catalog, "fake-model") })

	task := Tasks()[0] // fix-off-by-one
	p := &scriptedProvider{steps: []llm.Message{
		{Blocks: []llm.Block{toolUse("read", `{"path":"calc/calc.go"}`)}},
		{Blocks: []llm.Block{toolUse("edit", `{"path":"calc/calc.go","old_string":"for i := 1; i < len(xs); i++ {","new_string":"for i := 0; i < len(xs); i++ {"}`)}},
		{Blocks: []llm.Block{llm.TextBlock("Fixed the off-by-one bug.")}},
	}}

	res := Run(context.Background(), task, p, "fake-model", 30*time.Second)
	if !res.Pass {
		t.Fatalf("expected a pass, got fail: %s", res.Detail)
	}
	if res.ToolCalls != 2 {
		t.Errorf("tool calls = %d, want 2 (read, edit)", res.ToolCalls)
	}
	if res.CostUSD <= 0 {
		t.Errorf("cost should be positive for a priced model, got %v", res.CostUSD)
	}
	if res.Duration <= 0 {
		t.Errorf("duration should be recorded")
	}
	if len(p.requests) == 0 {
		t.Fatal("the provider got no request")
	}
	system := p.requests[0].System
	if !strings.Contains(system, "You are Larik, a coding agent") || !strings.Contains(system, "Working directory:") || !strings.Contains(system, "Today's date:") {
		t.Errorf("bench should send Larik's real base prompt: %q", system)
	}
	if strings.Contains(system, "Larik is a terminal coding agent written in Go") {
		t.Error("bench loaded this repository's AGENTS.md instead of only the fixture's instructions")
	}
}

func TestRunFailsWhenTheAgentDoesNothing(t *testing.T) {
	task := Tasks()[0]
	p := &scriptedProvider{steps: []llm.Message{{Blocks: []llm.Block{llm.TextBlock("I looked and everything seems fine.")}}}}

	res := Run(context.Background(), task, p, "m", 30*time.Second)
	if res.Pass {
		t.Fatal("a no-op run should fail Verify")
	}
	if !strings.Contains(res.Detail, "go test") {
		t.Errorf("detail should explain the failure, got %q", res.Detail)
	}
}

func TestRunReportsSetupFailure(t *testing.T) {
	task := Task{
		Name:   "broken-fixture",
		Setup:  func(dir string) error { return &os.PathError{Op: "setup", Path: dir, Err: os.ErrPermission} },
		Verify: func(string) (bool, string) { return false, "" },
	}
	res := Run(context.Background(), task, &scriptedProvider{}, "m", time.Second)
	if res.Pass || !strings.Contains(res.Detail, "setting up the fixture") {
		t.Errorf("result = %+v", res)
	}
}

func TestSurveyFixtureMatchesItsAnswer(t *testing.T) {
	dir := t.TempDir()
	want, err := writeSurvey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) < 10 || len(want) > 40 {
		t.Fatalf("the survey should have tens of answers, has %d", len(want))
	}
	// Recompute the answer from the files, the way a model would.
	var got []string
	paths, _ := filepath.Glob(filepath.Join(dir, "pkg*", "*.go"))
	for _, p := range paths {
		data, _ := os.ReadFile(p)
		lines := strings.Split(string(data), "\n")
		for i, l := range lines {
			name, ok := strings.CutPrefix(l, "func ")
			if !ok || name[0] < 'A' || name[0] > 'Z' {
				continue
			}
			if i == 0 || !strings.HasPrefix(lines[i-1], "//") {
				got = append(got, filepath.Base(filepath.Dir(p))+"."+name[:strings.IndexByte(name, '(')])
			}
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("fixture and answer disagree:\n%v\n%v", got, want)
	}
	if pass, _ := goTest(dir); !pass {
		t.Fatal("the survey fixture should compile")
	}
}

func TestSurveyRejectsUnsortedAnswer(t *testing.T) {
	dir := t.TempDir()
	task := undocumentedTask()
	if err := task.Setup(dir); err != nil {
		t.Fatal(err)
	}
	answer := surveyAnswer()
	slices.Reverse(answer)
	if err := os.WriteFile(filepath.Join(dir, "UNDOCUMENTED.txt"), []byte(strings.Join(answer, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if pass, _ := task.Verify(dir); pass {
		t.Fatal("an unsorted answer must not pass")
	}
}

func TestPersistentStateBenchmarkRequiresSeparateScripts(t *testing.T) {
	task := persistentStateTask()
	want, _ := json.Marshal(persistentStateAnswer())
	firstCode := `store("benchmark.aggregate", ` + string(want) + `)`
	secondCode := `tools.write({path: "STATE_RESULT.json", content: JSON.stringify(load("benchmark.aggregate"))})`
	first, _ := json.Marshal(map[string]string{"code": firstCode})
	second, _ := json.Marshal(map[string]string{"code": secondCode})
	p := &scriptedProvider{steps: []llm.Message{
		{Blocks: []llm.Block{toolUse("run_code", string(first))}},
		{Blocks: []llm.Block{toolUse("run_code", string(second))}},
		{Blocks: []llm.Block{llm.TextBlock("Done.")}},
	}}
	res := RunWith(context.Background(), task, p, "m", Options{Execution: tools.ExecCode, Timeout: 30 * time.Second})
	if !res.Pass {
		t.Fatalf("separate store/load scripts should pass: %s", res.Detail)
	}

	bothCode := firstCode + `; ` + secondCode
	both, _ := json.Marshal(map[string]string{"code": bothCode})
	p = &scriptedProvider{steps: []llm.Message{
		{Blocks: []llm.Block{toolUse("run_code", string(both))}},
		{Blocks: []llm.Block{llm.TextBlock("Done.")}},
	}}
	res = RunWith(context.Background(), task, p, "m", Options{Execution: tools.ExecCode, Timeout: 30 * time.Second})
	if res.Pass || !strings.Contains(res.Detail, "separate scripts") {
		t.Fatalf("one script should fail the benchmark requirement: %+v", res)
	}

	write, _ := json.Marshal(map[string]string{"path": "STATE_RESULT.json", "content": string(want)})
	p = &scriptedProvider{steps: []llm.Message{
		{Blocks: []llm.Block{toolUse("write", string(write))}},
		{Blocks: []llm.Block{llm.TextBlock("Done.")}},
	}}
	res = RunWith(context.Background(), task, p, "m", Options{Execution: tools.ExecTools, Timeout: 30 * time.Second})
	if !res.Pass {
		t.Fatalf("ordinary tools fallback should pass: %s", res.Detail)
	}
}

func TestRunWithCodeCountsInnerCalls(t *testing.T) {
	task := Tasks()[0] // fix-off-by-one
	code := `const src = tools.read({path: "calc/calc.go"}); tools.edit({path: "calc/calc.go", old_string: "for i := 1;", new_string: "for i := 0;"}); "ok"`
	in, _ := json.Marshal(map[string]string{"code": code})
	p := &scriptedProvider{steps: []llm.Message{
		{Blocks: []llm.Block{toolUse("run_code", string(in))}},
		{Blocks: []llm.Block{llm.TextBlock("Fixed.")}},
	}}
	res := RunWith(context.Background(), task, p, "m", Options{Execution: tools.ExecCode, Timeout: 30 * time.Second})
	if !res.Pass {
		t.Fatalf("expected a pass: %s", res.Detail)
	}
	if res.ToolCalls != 2 || res.Requests != 2 || res.PeakContext != 100 || res.Usage.Output != 40 || res.Execution != tools.ExecCode {
		t.Errorf("result = %+v", res)
	}
}

func TestKeepFailedKeepsTheTranscriptAndScriptCalls(t *testing.T) {
	task := Tasks()[0] // fix-off-by-one
	code := `tools.read({path: "calc/calc.go"}); tools.glob({pattern: "**/*.go"}).length`
	in, _ := json.Marshal(map[string]string{"code": code})
	p := &scriptedProvider{steps: []llm.Message{
		{Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "s1", Name: "run_code", Input: in}}},
		{Blocks: []llm.Block{llm.TextBlock("Looks fine to me.")}},
	}}
	res := RunWith(context.Background(), task, p, "m", Options{Execution: tools.ExecCode, Timeout: 30 * time.Second, KeepFailed: true})
	if res.Pass || res.Kept == "" {
		t.Fatalf("a failed run should be kept: %+v", res)
	}
	t.Cleanup(func() { os.RemoveAll(res.Kept) })
	if _, err := os.Stat(filepath.Join(res.Kept, "work", "calc", "calc.go")); err != nil {
		t.Errorf("the fixture should be kept as the agent left it: %v", err)
	}
	md, _ := os.ReadFile(filepath.Join(res.Kept, "transcript.md"))
	if !strings.Contains(string(md), "Looks fine to me.") || !strings.Contains(string(md), "tools.glob") {
		t.Errorf("transcript.md should hold the conversation and the script:\n%s", md)
	}
	calls, _ := os.ReadFile(filepath.Join(res.Kept, "calls.jsonl"))
	for _, want := range []string{`"id":"s1.1","tool":"read"`, `"id":"s1.2","tool":"glob"`, `"tool":"run_code"`} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("calls.jsonl lacks %s:\n%s", want, calls)
		}
	}
}

// failingProvider fails every request, as an API that rejects them does.
type failingProvider struct{ err error }

func (failingProvider) Name() string { return "fake" }

func (p failingProvider) Stream(context.Context, llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) { yield(llm.StreamEvent{}, p.err) }
}

// A run the provider ended says why: the bench used to show only
// "ended: error", which can't tell an outage from a rate limit.
func TestRunKeepsTheProvidersError(t *testing.T) {
	p := failingProvider{err: errors.New("429 Too Many Requests: rate limit reached for gpt-6-sol")}
	res := RunWith(context.Background(), Tasks()[0], p, "m", Options{Timeout: 30 * time.Second, KeepFailed: true})
	if res.Kept != "" {
		t.Cleanup(func() { os.RemoveAll(res.Kept) })
	}
	if res.Pass || res.Stop != "error" || !strings.Contains(res.Error, "rate limit reached") {
		t.Fatalf("stop %q, error %q", res.Stop, res.Error)
	}
	md, _ := os.ReadFile(filepath.Join(res.Kept, "transcript.md"))
	if !strings.Contains(string(md), "## Error") || !strings.Contains(string(md), "rate limit reached") {
		t.Errorf("transcript.md should end with the error:\n%s", md)
	}
}

func TestPassingRunsAreNotKept(t *testing.T) {
	task := Tasks()[0]
	p := &scriptedProvider{steps: []llm.Message{
		{Blocks: []llm.Block{toolUse("read", `{"path":"calc/calc.go"}`)}},
		{Blocks: []llm.Block{toolUse("edit", `{"path":"calc/calc.go","old_string":"for i := 1;","new_string":"for i := 0;"}`)}},
		{Blocks: []llm.Block{llm.TextBlock("Fixed.")}},
	}}
	res := RunWith(context.Background(), task, p, "m", Options{Timeout: 30 * time.Second, KeepFailed: true})
	if !res.Pass || res.Kept != "" {
		t.Fatalf("a passing run should be discarded: %+v", res)
	}
}

// A model that fumbles calls and then recovers still passes, which is why
// the fault counters exist: the pass rate alone would hide the fumbling.
func TestRunCountsMalformedCallsAlongsideAPass(t *testing.T) {
	task := Tasks()[0] // fix-off-by-one
	truncated := toolUse("read", "")
	truncated.Input = nil
	p := &scriptedProvider{steps: []llm.Message{
		{Blocks: []llm.Block{toolUse("no_such_tool", `{}`)}},
		{Blocks: []llm.Block{truncated}},
		{Blocks: []llm.Block{toolUse("read", `{"path":"calc/calc.go"}`)}},
		{Blocks: []llm.Block{toolUse("edit", `{"path":"calc/calc.go","old_string":"for i := 1; i < len(xs); i++ {","new_string":"for i := 0; i < len(xs); i++ {"}`)}},
		{Blocks: []llm.Block{llm.TextBlock("Fixed it, eventually.")}},
	}}

	res := Run(context.Background(), task, p, "m", 30*time.Second)
	if !res.Pass {
		t.Fatalf("expected a pass despite the bad calls: %s", res.Detail)
	}
	if res.FaultCalls != 2 || res.ToolCalls != 4 {
		t.Errorf("faults = %d of %d calls, want 2 of 4", res.FaultCalls, res.ToolCalls)
	}
	if res.Faults[agent.FaultUnknownTool] != 1 || res.Faults[agent.FaultInvalidJSON] != 1 {
		t.Errorf("breakdown = %v", res.Faults)
	}
	if got := res.FaultBreakdown(); got != "invalid_json 1, unknown_tool 1" {
		t.Errorf("FaultBreakdown() = %q", got)
	}
	if got := res.FaultRate(); got != 0.5 {
		t.Errorf("FaultRate() = %v, want 0.5", got)
	}
}

// A clean run must report nothing, so the metric stays quiet by default.
func TestACleanRunReportsNoFaults(t *testing.T) {
	task := Tasks()[0]
	p := &scriptedProvider{steps: []llm.Message{
		{Blocks: []llm.Block{toolUse("read", `{"path":"calc/calc.go"}`)}},
		{Blocks: []llm.Block{toolUse("edit", `{"path":"calc/calc.go","old_string":"for i := 1; i < len(xs); i++ {","new_string":"for i := 0; i < len(xs); i++ {"}`)}},
		{Blocks: []llm.Block{llm.TextBlock("Fixed.")}},
	}}
	res := Run(context.Background(), task, p, "m", 30*time.Second)
	if res.FaultCalls != 0 || res.FaultBreakdown() != "" || res.FaultRate() != 0 {
		t.Errorf("clean run reported %d faults (%q, rate %v)", res.FaultCalls, res.FaultBreakdown(), res.FaultRate())
	}
}

// The failure bad sampling produces: the model answers instead of acting.
// There is already a task-does-nothing case above; this pins the metric.
func TestRunFlagsAModelThatAnsweredWithoutActing(t *testing.T) {
	task := Tasks()[0] // fix-off-by-one
	p := &scriptedProvider{steps: []llm.Message{
		{Blocks: []llm.Block{llm.TextBlock("I looked and everything seems fine.")}}}}

	res := Run(context.Background(), task, p, "m", 30*time.Second)
	if res.Pass {
		t.Fatal("the task is not actually fixed")
	}
	if res.Stop != "end_turn" {
		t.Errorf("Stop = %q, want end_turn", res.Stop)
	}
	if !res.StoppedWithoutActing() || !res.NeverActed() {
		t.Errorf("expected both flags set: stopped=%v never=%v (%d calls)",
			res.StoppedWithoutActing(), res.NeverActed(), res.ToolCalls)
	}
	// Nothing was malformed: the model formed no call at all, which is why
	// the fault counters cannot see this.
	if res.FaultCalls != 0 {
		t.Errorf("faults = %d, want 0", res.FaultCalls)
	}
}

// Giving up partway is the same failure, but NeverActed is narrower.
func TestGivingUpPartwayIsNotNeverActed(t *testing.T) {
	task := Tasks()[0]
	p := &scriptedProvider{steps: []llm.Message{
		{Blocks: []llm.Block{toolUse("read", `{"path":"calc/calc.go"}`)}},
		{Blocks: []llm.Block{llm.TextBlock("This looks correct to me already.")}}}}

	res := Run(context.Background(), task, p, "m", 30*time.Second)
	if res.Pass {
		t.Fatal("the task is not actually fixed")
	}
	if !res.StoppedWithoutActing() {
		t.Errorf("a run abandoned partway should be flagged, Stop=%q", res.Stop)
	}
	if res.NeverActed() {
		t.Error("it did call a tool, so NeverActed should be false")
	}
}

// A successful run also ends in prose, which is how an agent says it is
// done. Flagging that would make the metric useless.
func TestASuccessfulRunIsNotFlagged(t *testing.T) {
	task := Tasks()[0]
	p := &scriptedProvider{steps: []llm.Message{
		{Blocks: []llm.Block{toolUse("read", `{"path":"calc/calc.go"}`)}},
		{Blocks: []llm.Block{toolUse("edit", `{"path":"calc/calc.go","old_string":"for i := 1; i < len(xs); i++ {","new_string":"for i := 0; i < len(xs); i++ {"}`)}},
		{Blocks: []llm.Block{llm.TextBlock("Fixed the off-by-one.")}},
	}}
	res := Run(context.Background(), task, p, "m", 30*time.Second)
	if !res.Pass {
		t.Fatalf("expected a pass: %s", res.Detail)
	}
	if res.Stop != "end_turn" {
		t.Errorf("Stop = %q, want end_turn", res.Stop)
	}
	if res.StoppedWithoutActing() || res.NeverActed() {
		t.Error("a passing run must not be flagged for ending in prose")
	}
}

// A timeout is not the model declining to act, so it must not be counted
// as one; the distinction is the whole point of the metric.
func TestATimeoutIsNotCountedAsStoppingWithoutActing(t *testing.T) {
	task := Tasks()[0]
	var steps []llm.Message
	for range 50 {
		steps = append(steps, llm.Message{Blocks: []llm.Block{toolUse("read", `{"path":"calc/calc.go"}`)}})
	}
	p := &scriptedProvider{steps: steps}

	res := Run(context.Background(), task, p, "m", 300*time.Millisecond)
	if res.Pass {
		t.Fatal("expected a failure")
	}
	if res.StoppedWithoutActing() {
		t.Errorf("a timeout was counted as the model stopping, Stop=%q", res.Stop)
	}
}

func TestScriptStateNeedsStoreThenLaterLoad(t *testing.T) {
	cases := []struct {
		scripts []string
		want    bool
	}{
		{[]string{`store("k", 1)`, `load("k")`}, true},
		{[]string{`store("k", 1)`, `const v = load("k"); store("k", v)`}, true},
		{[]string{`load("k")`, `store("k", 1)`}, false},
		{[]string{`store("k", 1); load("k")`}, false},
		{[]string{`restore("k")`, `download("k")`}, false},
		{[]string{`store ("k", 1)`, `x.load("k")`}, false},
	}
	for _, c := range cases {
		var s scriptState
		for _, code := range c.scripts {
			s.see(code)
		}
		if s.loaded != c.want {
			t.Errorf("%q: loaded = %v, want %v", c.scripts, s.loaded, c.want)
		}
	}
}
