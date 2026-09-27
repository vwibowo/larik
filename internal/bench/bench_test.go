package bench

import (
	"context"
	"encoding/json"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"larik/internal/llm"
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
