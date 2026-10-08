package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"larik/internal/session"
)

// scriptCaller answers like fakeCaller, plus "exit", which fails with
// Data, and runs batches concurrently, recording how many ran at once.
type scriptCaller struct {
	mu      sync.Mutex
	calls   []string
	batches int
	active  int
	peak    int
}

func (c *scriptCaller) CallTool(ctx context.Context, name string, input json.RawMessage) Result {
	c.mu.Lock()
	c.calls = append(c.calls, name)
	c.active++
	c.peak = max(c.peak, c.active)
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.active--; c.mu.Unlock() }()
	time.Sleep(30 * time.Millisecond)
	switch name {
	case "fail":
		return Result{Content: "it broke", IsError: true}
	case "exit":
		return Result{Content: "out\n[exit code 3]", IsError: true, Data: map[string]any{"output": "out\n", "exit_code": 3}}
	}
	return Result{Content: "got " + string(input)}
}

func (c *scriptCaller) CallTools(ctx context.Context, calls []Call) []Result {
	c.mu.Lock()
	c.batches++
	c.mu.Unlock()
	out := make([]Result, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = c.CallTool(ctx, call.Name, call.Input)
		}()
	}
	wg.Wait()
	return out
}

func scriptRegistry() *Registry {
	return NewRegistry(
		codeStub{"echo", `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`},
		codeStub{"fail", `{"type":"object","properties":{}}`},
		codeStub{"exit", `{"type":"object","properties":{}}`},
		codeStub{"mcp__my-srv__ping", `{"type":"object","properties":{"host":{"type":"string"}},"required":["host"]}`},
		// Its identifier would be a_b, which another tool has.
		codeStub{"a-b", `{"type":"object","properties":{}}`},
		codeStub{"a_b", `{"type":"object","properties":{}}`},
	)
}

func runScript(t *testing.T, ctx context.Context, env *Env, c Caller, code string) Result {
	t.Helper()
	tool, ok := scriptRegistry().ForExecution(ExecCode).Get(CodeToolName)
	if !ok {
		t.Fatal("no run_code")
	}
	in, _ := json.Marshal(map[string]any{"code": code})
	return tool.Run(WithCaller(ctx, c), env, in)
}

func TestScriptToolNamesAreIdentifiers(t *testing.T) {
	desc := specOf(t, scriptRegistry().ForExecution(ExecCode), CodeToolName).Description
	if !strings.Contains(desc, "- mcp__my_srv__ping({host}):") || !strings.Contains(desc, "- a-b({}):") {
		t.Fatalf("the index should name tools as scripts call them:\n%s", desc)
	}
	c := &scriptCaller{}
	res := runScript(t, context.Background(), nil, c, `
console.log(tools.mcp__my_srv__ping({host: "a"}))
console.log(tools.call("mcp__my-srv__ping", {host: "b"}))
console.log(tools.call("mcp__my_srv__ping", {host: "c"}))
console.log(tools.describe("mcp__my-srv__ping").name)
console.log(typeof tools.list, tools.list().join(","))
tools["a-b"]({})`)
	if res.IsError {
		t.Fatalf("unexpected error:\n%s", res.Content)
	}
	for _, want := range []string{`got {"host":"a"}`, `got {"host":"b"}`, `got {"host":"c"}`, "\nmcp__my_srv__ping\n", "function a-b,a_b,echo,exit,fail,mcp__my_srv__ping"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("missing %q in:\n%s", want, res.Content)
		}
	}
	if strings.Join(c.calls, ",") != "mcp__my-srv__ping,mcp__my-srv__ping,mcp__my-srv__ping,a-b" {
		t.Errorf("the parent should get the tools' own names: %v", c.calls)
	}
}

func TestScriptSearch(t *testing.T) {
	c := &scriptCaller{}
	res := runScript(t, context.Background(), nil, c, `
const hits = tools.search("ping host");
console.log(hits.length, hits[0].name, hits[0].signature, hits[0].description);
console.log(tools.search("tool", 2).length, tools.search("zzz").length, tools.search("tool", 0).length);
tools[hits[0].name]({host: "h"})`)
	if res.IsError {
		t.Fatalf("unexpected error:\n%s", res.Content)
	}
	for _, want := range []string{"1 mcp__my_srv__ping mcp__my_srv__ping({host}) mcp__my-srv__ping tool", "2 0 1", `=> got {"host":"h"}`} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("missing %q in:\n%s", want, res.Content)
		}
	}
	if len(c.calls) != 1 {
		t.Errorf("search should not call tools: %v", c.calls)
	}
}

func TestScriptGetsDataInsteadOfAnError(t *testing.T) {
	res := runScript(t, context.Background(), nil, &scriptCaller{}, `const r = tools.exit({}); console.log(r.exit_code, JSON.stringify(r.output))`)
	if res.IsError || !strings.Contains(res.Content, `3 "out\n"`) {
		t.Fatalf("a result with Data should not throw:\n%s", res.Content)
	}
}

func TestScriptParallel(t *testing.T) {
	c := &scriptCaller{}
	res := runScript(t, context.Background(), nil, c, `
const rs = tools.parallel([
  {name: "echo", args: {text: "a"}},
  {name: "fail", args: {}},
  {name: "nope"},
  {tool: "exit"},
  {name: "echo", args: {text: "b"}},
  {},
]);
rs.map(r => r.ok ? (typeof r.value === "string" ? r.value : "exit " + r.value.exit_code) : "error: " + r.error).join("\n")`)
	if res.IsError {
		t.Fatalf("unexpected error:\n%s", res.Content)
	}
	for _, want := range []string{`got {"text":"a"}`, "error: fail: it broke", `error: no tool named "nope"`, "exit 3", `got {"text":"b"}`, "error: entry 5: want {name, args}", "[run_code: 4 tool calls]"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("missing %q in:\n%s", want, res.Content)
		}
	}
	if c.batches != 1 || c.peak != 4 {
		t.Errorf("batches = %d, peak concurrency = %d; want 1 batch of 4 at once", c.batches, c.peak)
	}
	// A caller without batches runs them one at a time.
	f := &fakeCaller{}
	res = runScript(t, context.Background(), nil, f, `tools.parallel([{name: "echo", args: {text: "x"}}, {name: "echo", args: {text: "y"}}]).map(r => r.value).join("|")`)
	if res.IsError || !strings.Contains(res.Content, `got {"text":"x"}|got {"text":"y"}`) || len(f.calls) != 2 {
		t.Fatalf("sequential fallback:\n%s", res.Content)
	}
	if res := runScript(t, context.Background(), nil, f, `tools.parallel("echo")`); !res.IsError || !strings.Contains(res.Content, "array of {name, args}") {
		t.Fatalf("a non-array should throw:\n%s", res.Content)
	}
}

func TestScriptParallelCountsTowardTheLimit(t *testing.T) {
	c := &scriptCaller{}
	res := runScript(t, context.Background(), nil, c, `
for (let i = 0; i < 199; i++) tools.call("echo", {text: "x"});
tools.parallel([{name: "echo", args: {text: "y"}}, {name: "echo", args: {text: "z"}}]).map(r => r.ok + " " + (r.error || "")).join("\n")`)
	if !strings.Contains(res.Content, "true \nfalse the script reached its limit") || len(c.calls) != codeMaxCalls {
		t.Fatalf("calls = %d:\n%s", len(c.calls), res.Content)
	}
}

func TestLongScriptOutputIsSaved(t *testing.T) {
	dir := t.TempDir()
	env := NewEnv(dir)
	env.RawOutputDir = filepath.Join(dir, "raw")
	ctx := WithCallID(context.Background(), "c7")
	res := runScript(t, ctx, env, &scriptCaller{}, `for (let i = 0; i < 300000; i++) console.log("line " + i); "the end"`)
	if res.IsError || len(res.Content) > MaxOutputBytes+300 {
		t.Fatalf("result: %d bytes, error %v", len(res.Content), res.IsError)
	}
	for _, want := range []string{"line 0\n", "bytes truncated", "=> the end", "raw_output tool_call_id=c7]"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("missing %q in the result's ends:\n%s\n...\n%s", want, res.Content[:200], res.Content[len(res.Content)-300:])
		}
	}
	data, err := os.ReadFile(filepath.Join(env.RawOutputDir, session.RawName("c7")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "line 0\nline 1\n") || !strings.Contains(string(data), "\nline 150000\n") || !strings.HasSuffix(string(data), "line 299999\n=> the end") {
		t.Fatalf("the saved output should be complete: %d bytes", len(data))
	}
	got := (RawOutput{}).Run(context.Background(), env, json.RawMessage(`{"tool_call_id":"c7","offset":0,"limit":100}`))
	if got.IsError || !strings.Contains(got.Content, "line 0") {
		t.Fatalf("raw_output: %+v", got)
	}

	// Short output isn't saved.
	ctx = WithCallID(context.Background(), "c8")
	if res := runScript(t, ctx, env, &scriptCaller{}, `console.log("short")`); strings.Contains(res.Content, "raw_output") {
		t.Fatalf("short output: %s", res.Content)
	}
	if _, err := os.Stat(filepath.Join(env.RawOutputDir, session.RawName("c8"))); !os.IsNotExist(err) {
		t.Fatal("short output should leave no file")
	}
}

func TestScriptOutputLimitKeepsTheEnd(t *testing.T) {
	// Past the output limit, the stop marker and the error still show.
	res := runScript(t, context.Background(), nil, &scriptCaller{}, `
const line = "y".repeat(1023);
for (let i = 0; i < 17 * 1024; i++) console.log(line);
throw new Error("after the flood")`)
	if !res.IsError || !strings.Contains(res.Content, "[output stopped at 16 MB]") || !strings.Contains(res.Content, "after the flood") {
		t.Fatalf("the end of the result: %s", res.Content[max(0, len(res.Content)-400):])
	}
}

func TestBashInScriptReturnsData(t *testing.T) {
	env := NewEnv(t.TempDir())
	ctx := context.WithValue(context.Background(), scriptKey{}, true)
	res := (Bash{}).Run(ctx, env, json.RawMessage(`{"command":"echo hi; exit 3"}`))
	d, ok := res.Data.(map[string]any)
	if !res.IsError || !strings.Contains(res.Content, "[exit code 3]") || !ok || d["exit_code"] != 3 || d["output"] != "hi\n" || d["truncated"] != false {
		t.Fatalf("result: %+v", res)
	}
	// A script gets up to scriptBashOutput, the model MaxOutputBytes.
	res = (Bash{}).Run(ctx, env, json.RawMessage(`{"command":"seq 1 100000"}`))
	d, _ = res.Data.(map[string]any)
	out, _ := d["output"].(string)
	if res.IsError || len(res.Content) > MaxOutputBytes+200 || !strings.HasPrefix(out, "1\n2\n") || !strings.HasSuffix(out, "\n100000\n") || strings.Contains(out, "truncated") || d["truncated"] != false {
		t.Fatalf("content %d bytes, script output %d bytes, truncated %v", len(res.Content), len(out), d["truncated"])
	}
	res = (Bash{}).Run(ctx, env, json.RawMessage(`{"command":"seq 1 400000"}`))
	d, _ = res.Data.(map[string]any)
	out, _ = d["output"].(string)
	if len(out) > scriptBashOutput+200 || !strings.HasSuffix(out, "\n400000\n") || d["truncated"] != true {
		t.Fatalf("script output %d bytes, truncated %v", len(out), d["truncated"])
	}
	// Outside a script there is no Data.
	if res := (Bash{}).Run(context.Background(), env, json.RawMessage(`{"command":"echo hi"}`)); res.Data != nil {
		t.Fatalf("data outside a script: %+v", res.Data)
	}
}

func TestJSName(t *testing.T) {
	for in, want := range map[string]string{"read": "read", "mcp__a-b__c.d": "mcp__a_b__c_d", "9x": "_x", "a9$": "a9$"} {
		if got := jsName(in); got != want {
			t.Errorf("jsName(%q) = %q, want %q", in, got, want)
		}
	}
}
