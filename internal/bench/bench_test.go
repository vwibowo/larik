package bench

import (
	"context"
	"encoding/json"
	"iter"
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
type scriptedProvider struct{ steps []llm.Message }

func (scriptedProvider) Name() string { return "fake" }

func (p *scriptedProvider) Stream(_ context.Context, req llm.Request) iter.Seq2[llm.StreamEvent, error] {
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
