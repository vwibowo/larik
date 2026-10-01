package tools_test

// This test lives outside package tools because the sandbox package
// imports it (through web).

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/sandbox"
	"larik/internal/tools"
)

type echoTool struct{}

func (echoTool) ReadOnly() bool { return true }
func (echoTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: "echo", Schema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)}
}
func (echoTool) Run(context.Context, *tools.Env, json.RawMessage) tools.Result { return tools.Result{} }

// echoCaller answers every call with its input.
type echoCaller struct{ calls int }

func (c *echoCaller) CallTool(_ context.Context, name string, input json.RawMessage) tools.Result {
	c.calls++
	return tools.Result{Content: name + " got " + string(input)}
}

// recordingSandbox notes that the runner was confined.
type recordingSandbox struct {
	*sandbox.Sandbox
	confined int
}

func (r *recordingSandbox) Confine(argv []string) []string {
	r.confined++
	return r.Sandbox.Confine(argv)
}

func TestRunCodeInsideTheSandbox(t *testing.T) {
	home, _ := os.UserHomeDir()
	sb, warn := sandbox.New(sandbox.Config{}, t.TempDir(), home)
	if sb == nil {
		t.Skipf("no sandbox on this machine: %s", warn)
	}
	t.Cleanup(func() { sb.Close() })
	env := tools.NewEnv(t.TempDir())
	rec := &recordingSandbox{Sandbox: sb}
	env.Sandbox = rec
	tool, _ := tools.NewRegistry(echoTool{}).ForExecution(tools.ExecHybrid).Get(tools.CodeToolName)
	run := func(c tools.Caller, code string) tools.Result {
		in, _ := json.Marshal(map[string]any{"code": code, "timeout_seconds": 60})
		return tool.Run(tools.WithCaller(context.Background(), c), env, in)
	}

	c := &echoCaller{}
	res := run(c, `console.log(tools.echo({text: "in"})); 6 * 7`)
	if res.IsError || !strings.Contains(res.Content, `echo got {"text":"in"}`) || !strings.Contains(res.Content, "=> 42") || c.calls != 1 {
		t.Fatalf("a confined script should run and reach its tools:\n%s", res.Content)
	}
	if rec.confined != 1 {
		t.Fatalf("the runner should be started through the sandbox (Confine called %d times)", rec.confined)
	}

	tools.SetCodeMemoryLimit(t, 64)
	res = run(&echoCaller{}, `Array(1e9).fill(0)`)
	if !res.IsError || !strings.Contains(res.Content, "more than 64 MB") {
		t.Fatalf("the memory limit should hold inside the sandbox:\n%s", res.Content)
	}
}
