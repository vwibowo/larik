package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"larik/internal/llm"
)

// fakeCaller answers echo and fail, and records the calls.
type fakeCaller struct {
	calls []string
	wait  time.Duration
}

func (f *fakeCaller) CallTool(ctx context.Context, name string, input json.RawMessage) Result {
	f.calls = append(f.calls, name+" "+string(input))
	if f.wait > 0 {
		select {
		case <-time.After(f.wait):
		case <-ctx.Done():
			return Result{Content: "interrupted", IsError: true}
		}
	}
	if name == "fail" {
		return Result{Content: "it broke", IsError: true}
	}
	return Result{Content: "got " + string(input)}
}

type codeStub struct{ name, schema string }

func (s codeStub) ReadOnly() bool { return true }
func (s codeStub) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: s.name, Description: s.name + " tool", Schema: schema(s.schema)}
}
func (codeStub) Run(context.Context, *Env, json.RawMessage) Result { return Result{} }

func codeRegistry() *Registry {
	return NewRegistry(
		codeStub{"echo", `{"type":"object","properties":{"text":{"type":"string"},"loud":{"type":"boolean"}},"required":["text"]}`},
		codeStub{"fail", `{"type":"object","properties":{}}`},
		codeStub{"todo_write", `{"type":"object","properties":{}}`},
	)
}

func runCode(t *testing.T, c Caller, code string, timeout int) Result {
	t.Helper()
	tool, ok := codeRegistry().ForExecution(ExecHybrid).Get(CodeToolName)
	if !ok {
		t.Fatal("hybrid registry has no run_code")
	}
	in, _ := json.Marshal(map[string]any{"code": code, "timeout_seconds": timeout})
	ctx := context.Background()
	if c != nil {
		ctx = WithCaller(ctx, c)
	}
	return tool.Run(ctx, nil, in)
}

func TestForExecution(t *testing.T) {
	r := codeRegistry()
	if got := r.ForExecution(ExecTools); got != r {
		t.Fatal("tools execution should return the registry unchanged")
	}
	hybrid := r.ForExecution(ExecHybrid)
	if n := len(hybrid.Specs()); n != 4 {
		t.Fatalf("hybrid has %d specs, want the 3 tools plus run_code", n)
	}
	code := r.ForExecution(ExecCode)
	specs := code.Specs()
	if len(specs) != 1 || specs[0].Name != CodeToolName {
		t.Fatalf("code execution specs = %v, want run_code only", specs)
	}
	if _, ok := code.Get("echo"); !ok {
		t.Fatal("code execution must still resolve tools by name for scripts")
	}
	desc := specs[0].Description
	if !strings.Contains(desc, "echo({text, loud?})") {
		t.Fatalf("description lacks the echo signature:\n%s", desc)
	}
	if strings.Contains(desc, "todo_write") {
		t.Fatal("todo_write must not be bound to scripts")
	}

	// Hybrid doesn't repeat the schemas the model already has, but lists
	// the deferred tools it doesn't.
	hdesc := specOf(t, hybrid, CodeToolName).Description
	if strings.Contains(hdesc, "echo(") || !strings.Contains(hdesc, "Every tool you have works this way") {
		t.Fatalf("hybrid should not index declared tools:\n%s", hdesc)
	}
	h, c := specOf(t, Default().ForExecution(ExecHybrid), CodeToolName), specOf(t, Default().ForExecution(ExecCode), CodeToolName)
	if len(h.Description)*3 > len(c.Description)*2 {
		t.Fatalf("with the built-in tools, hybrid's description (%d) should be well under code's (%d)", len(h.Description), len(c.Description))
	}
	deferred := NewRegistry(codeStub{"echo", `{"type":"object"}`}).Defer(codeStub{"mcp__x__ping", `{"type":"object","properties":{"host":{"type":"string"}},"required":["host"]}`})
	ddesc := specOf(t, deferred.ForExecution(ExecHybrid), CodeToolName).Description
	if !strings.Contains(ddesc, "mcp__x__ping({host})") || strings.Contains(ddesc, "echo(") {
		t.Fatalf("hybrid should index only deferred tools:\n%s", ddesc)
	}
}

func specOf(t *testing.T, r *Registry, name string) llm.ToolSpec {
	t.Helper()
	tool, ok := r.Get(name)
	if !ok {
		t.Fatalf("no %s", name)
	}
	return tool.Spec()
}

func TestParseExecution(t *testing.T) {
	for in, want := range map[string]Execution{"": ExecTools, "tools": ExecTools, "hybrid": ExecHybrid, "code": ExecCode} {
		if got, err := ParseExecution(in); err != nil || got != want {
			t.Fatalf("ParseExecution(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseExecution("fast"); err == nil {
		t.Fatal("expected an error for an unknown execution")
	}
}

func TestRunCodeCallsTools(t *testing.T) {
	c := &fakeCaller{}
	res := runCode(t, c, `
const out = [];
for (const w of ["a", "b"]) out.push(tools.echo({text: w}));
console.log(out.join("|"));
({count: out.length})`, 0)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if !strings.Contains(res.Content, `got {"text":"a"}|got {"text":"b"}`) {
		t.Fatalf("missing printed output:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, `"count": 2`) || !strings.Contains(res.Content, "[run_code: 2 tool calls]") {
		t.Fatalf("missing return value or footer:\n%s", res.Content)
	}
	if len(c.calls) != 2 {
		t.Fatalf("calls = %v", c.calls)
	}
}

func TestRunCodeToolErrorsThrow(t *testing.T) {
	res := runCode(t, &fakeCaller{}, `
try { tools.fail({}) } catch (e) { console.log("caught:", e.message) }
tools.fail({})`, 0)
	if !res.IsError {
		t.Fatalf("an uncaught tool error should fail the script:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "caught: fail: it broke") {
		t.Fatalf("catch should see the tool's message:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "script.js:3") {
		t.Fatalf("the error should name the line:\n%s", res.Content)
	}
}

func TestRunCodeRejectsUnboundTools(t *testing.T) {
	c := &fakeCaller{}
	res := runCode(t, c, `tools.call("todo_write", {})`, 0)
	if !res.IsError || len(c.calls) != 0 {
		t.Fatalf("todo_write must not run from a script: %v\n%s", c.calls, res.Content)
	}
	res = runCode(t, c, `tools.call("run_code", {code: "1"})`, 0)
	if !res.IsError || len(c.calls) != 0 {
		t.Fatalf("run_code must not run from a script: %v\n%s", c.calls, res.Content)
	}
}

func TestRunCodeTimeout(t *testing.T) {
	start := time.Now()
	res := runCode(t, &fakeCaller{}, `while (true) {}`, 1)
	if !res.IsError || !strings.Contains(res.Content, "timed out") {
		t.Fatalf("an endless loop should time out:\n%s", res.Content)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout took too long")
	}
}

func TestRunCodeCallLimit(t *testing.T) {
	c := &fakeCaller{}
	res := runCode(t, c, `for (let i = 0; i < 1000; i++) tools.echo({text: "x"})`, 0)
	if !res.IsError || !strings.Contains(res.Content, fmt.Sprintf("limit of %d", codeMaxCalls)) || len(c.calls) != codeMaxCalls {
		t.Fatalf("calls = %d:\n%s", len(c.calls), res.Content)
	}
}

func TestRunCodeOutputCapped(t *testing.T) {
	res := runCode(t, &fakeCaller{}, `for (let i = 0; i < 20000; i++) console.log("line " + i + " " + "x".repeat(40))`, 0)
	if len(res.Content) > MaxOutputBytes+200 || !strings.Contains(res.Content, "bytes truncated") {
		t.Fatalf("output not capped: %d bytes", len(res.Content))
	}
	if !strings.Contains(res.Content, "line 19999") {
		t.Fatal("the tail of the output should be kept")
	}
}

func TestRunCodeTopLevelAwait(t *testing.T) {
	res := runCode(t, &fakeCaller{}, `const r = await tools.echo({text: "hi"}); console.log(r)`, 0)
	if res.IsError || !strings.Contains(res.Content, `got {"text":"hi"}`) {
		t.Fatalf("top-level await should work:\n%s", res.Content)
	}
}

func TestRunCodeSyntaxError(t *testing.T) {
	res := runCode(t, &fakeCaller{}, `const = 1`, 0)
	if !res.IsError || !strings.Contains(res.Content, "script.js") {
		t.Fatalf("a syntax error should be reported with its position:\n%s", res.Content)
	}
}

func TestRunCodeWithoutCaller(t *testing.T) {
	if res := runCode(t, nil, `1`, 0); !res.IsError {
		t.Fatal("run_code without a caller should fail")
	}
}

func TestRunCodeDescribe(t *testing.T) {
	res := runCode(t, &fakeCaller{}, `tools.describe("echo").schema.required[0] + " " + tools.list().join(",")`, 0)
	if res.IsError || !strings.Contains(res.Content, "=> text echo,fail") {
		t.Fatalf("describe/list:\n%s", res.Content)
	}
}
