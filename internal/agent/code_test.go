package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"larik/internal/hooks"
	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/tools"
)

func runCodeUse(id, code string) llm.Block {
	in, _ := json.Marshal(map[string]string{"code": code})
	return toolUse(id, tools.CodeToolName, string(in))
}

func TestRunCodeCallsAreAuthorizedOneByOne(t *testing.T) {
	a, fp, dir := setup(t, permission.ModeDefault,
		assistant(runCodeUse("c1", `tools.write({path: "a.txt", content: "A"}); console.log(tools.read({path: "a.txt"}))`)),
		assistant(llm.TextBlock("done")))
	a.SetExecution(tools.ExecHybrid)
	evs := drain(a.Run(context.Background(), "go"), PermissionReply{Allow: true})

	if data, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(data) != "A" {
		t.Fatalf("the script's write should have run after approval: %q", data)
	}
	asked := permissionEvents(evs)
	if len(asked) != 1 || asked[0].ToolName != "write" || asked[0].ToolID != "c1.1" {
		t.Fatalf("the user should be asked about the inner write only: %+v", asked)
	}
	var started []string
	for _, e := range evs {
		if e.Kind == EvToolStart {
			started = append(started, e.ToolID+"="+e.ToolName)
		}
	}
	if strings.Join(started, ",") != "c1=run_code,c1.1=write,c1.2=read" {
		t.Errorf("tool starts = %v", started)
	}
	// Only the script's output goes back to the model.
	res := lastResult(fp, 1)
	if res.IsError || res.ID != "c1" || res.Name != tools.CodeToolName || !strings.Contains(res.Content, "A") || !strings.Contains(res.Content, "2 tool calls") {
		t.Errorf("result = %+v", res)
	}
	var declared []string
	for _, sp := range fp.requests[0].Tools {
		declared = append(declared, sp.Name)
	}
	if !strings.Contains(strings.Join(declared, ","), "read") || declared[len(declared)-1] != tools.CodeToolName {
		t.Errorf("hybrid declares the tools plus run_code: %v", declared)
	}
}

func TestRunCodeRespectsPlanMode(t *testing.T) {
	a, fp, dir := setup(t, permission.ModePlan,
		assistant(runCodeUse("c1", `
try { tools.write({path: "b.txt", content: "B"}) } catch (e) { console.log("denied:", e.message) }
tools.glob({pattern: "*"}).length >= 0`)),
		assistant(llm.TextBlock("done")))
	a.SetExecution(tools.ExecHybrid)
	drain(a.Run(context.Background(), "go"), PermissionReply{Allow: true})

	if _, err := os.Stat(filepath.Join(dir, "b.txt")); !os.IsNotExist(err) {
		t.Fatal("plan mode must block writes made by scripts")
	}
	if res := lastResult(fp, 1); res.IsError || !strings.Contains(res.Content, "denied: write: ") || !strings.Contains(res.Content, "plan mode is active") {
		t.Errorf("the script should see the denial and go on with read-only calls: %+v", res)
	}
}

func TestRunCodeCallsRunHooks(t *testing.T) {
	a, fp, dir := withHooks(t, permission.ModeYolo, permission.Rules{},
		hook(hooks.PreToolUse, "write", `echo "no writes from scripts" >&2; exit 2`),
		assistant(runCodeUse("c1", `tools.write({path: "c.txt", content: "C"})`)),
		assistant(llm.TextBlock("done")))
	a.SetExecution(tools.ExecHybrid)
	drain(a.Run(context.Background(), "go"), PermissionReply{})

	if _, err := os.Stat(filepath.Join(dir, "c.txt")); !os.IsNotExist(err) {
		t.Fatal("a PreToolUse hook on write must apply inside scripts")
	}
	if res := lastResult(fp, 1); !res.IsError || !strings.Contains(res.Content, "no writes from scripts") {
		t.Errorf("result = %+v", res)
	}
}

func TestExecutionSetsTheDeclaredTools(t *testing.T) {
	a, fp, _ := setup(t, permission.ModeYolo,
		assistant(runCodeUse("c1", `1`)), assistant(llm.TextBlock("done")),
		assistant(runCodeUse("c2", `tools.read({path: "missing.txt"})`)), assistant(llm.TextBlock("done")))
	if a.Execution() != tools.ExecTools {
		t.Fatalf("default execution = %q", a.Execution())
	}
	// With tools only, run_code doesn't exist.
	drain(a.Run(context.Background(), "go"), PermissionReply{})
	if res := lastResult(fp, 1); !res.IsError || !strings.Contains(res.Content, "unknown tool") {
		t.Errorf("run_code should be unknown under tools execution: %+v", res)
	}
	// With code, it is the only declared tool, and scripts reach the rest.
	a.SetExecution(tools.ExecCode)
	if a.Execution() != tools.ExecTools {
		t.Fatal("execution must stay fixed until the next fresh context")
	}
	a.Clear()
	drain(a.Run(context.Background(), "go"), PermissionReply{})
	if specs := fp.requests[2].Tools; len(specs) != 1 || specs[0].Name != tools.CodeToolName {
		t.Errorf("code execution declares %+v", specs)
	}
	if res := lastResult(fp, 3); !res.IsError || !strings.Contains(res.Content, "read: ") {
		t.Errorf("the script's read should have run and failed: %+v", res)
	}
}

func TestCodeExecutionRejectsDirectUndeclaredTool(t *testing.T) {
	a, fp, _ := setup(t, permission.ModeYolo,
		assistant(toolUse("r1", "read", `{"path":"missing.txt"}`)),
		assistant(llm.TextBlock("done")))
	a.SetExecution(tools.ExecCode)
	drain(a.Run(context.Background(), "go"), PermissionReply{})
	if res := lastResult(fp, 1); !res.IsError || !strings.Contains(res.Content, "unknown tool: read") {
		t.Fatalf("a direct tool call in code execution must be rejected: %+v", res)
	}
}

func TestExecutionChangeWaitsForClear(t *testing.T) {
	a, fp, _ := setup(t, permission.ModeYolo,
		assistant(llm.TextBlock("first")),
		assistant(llm.TextBlock("second")),
		assistant(llm.TextBlock("third")))
	drain(a.Run(context.Background(), "first"), PermissionReply{})
	a.SetExecution(tools.ExecCode)
	drain(a.Run(context.Background(), "second"), PermissionReply{})
	if a.Execution() != tools.ExecTools || len(fp.requests[0].Tools) != len(fp.requests[1].Tools) {
		t.Fatal("a saved execution change must not alter the current context")
	}
	a.Clear()
	drain(a.Run(context.Background(), "third"), PermissionReply{})
	if a.Execution() != tools.ExecCode || len(fp.requests[2].Tools) != 1 || fp.requests[2].Tools[0].Name != tools.CodeToolName {
		t.Fatalf("fresh context tools = %+v", fp.requests[2].Tools)
	}
}

func TestModelSwitchKeepsToolsUntilFreshContext(t *testing.T) {
	a, fp, _ := setup(t, permission.ModeYolo,
		assistant(llm.TextBlock("first")), assistant(llm.TextBlock("second")), assistant(llm.TextBlock("third")))
	a.opts.ExecutionFor = strongCode
	drain(a.Run(context.Background(), "first"), PermissionReply{})
	a.SetModel(fp, "strong")
	if a.Execution() != tools.ExecTools {
		t.Fatal("model switch changed tools mid-context")
	}
	drain(a.Run(context.Background(), "second"), PermissionReply{})
	if len(fp.requests[0].Tools) != len(fp.requests[1].Tools) {
		t.Fatal("tool specs changed mid-context")
	}
	a.Clear()
	drain(a.Run(context.Background(), "third"), PermissionReply{})
	if a.Execution() != tools.ExecCode || len(fp.requests[2].Tools) != 1 || fp.requests[2].Tools[0].Name != tools.CodeToolName {
		t.Fatalf("fresh context tools = %+v", fp.requests[2].Tools)
	}
}

func TestSubagentsInheritExecution(t *testing.T) {
	parent := New(Options{Provider: &fakeProvider{}, Model: "m", Cwd: t.TempDir(), Tools: tools.Default(), Execution: tools.ExecHybrid})
	child := parent.Spawn(SpawnOptions{Type: "worker", Provider: &fakeProvider{}, Model: "m", Tools: tools.Default()})
	if child.Execution() != tools.ExecHybrid {
		t.Errorf("child execution = %q", child.Execution())
	}
}

// strongCode gives "fake/strong" scripts only and every other model tools.
func strongCode(provider, model string) tools.Execution {
	if provider+"/"+model == "fake/strong" {
		return tools.ExecCode
	}
	return tools.ExecTools
}

func TestExecutionFollowsTheModel(t *testing.T) {
	a := New(Options{Provider: &fakeProvider{}, Model: "strong", Cwd: t.TempDir(), Tools: tools.Default(),
		Execution: tools.ExecCode, ExecutionFor: strongCode})
	a.SetModel(&fakeProvider{}, "small")
	if a.Execution() != tools.ExecTools {
		t.Errorf("switching to a model without scripts: %q", a.Execution())
	}
	a.SetModel(&fakeProvider{}, "strong")
	if a.Execution() != tools.ExecCode {
		t.Errorf("switching back: %q", a.Execution())
	}
	// A subagent on another model gets that model's setting, and keeps
	// following models itself.
	child := a.Spawn(SpawnOptions{Type: "worker", Provider: &fakeProvider{}, Model: "small", Tools: tools.Default()})
	if child.Execution() != tools.ExecTools {
		t.Errorf("worker on the small model: %q", child.Execution())
	}
	child.SetModel(&fakeProvider{}, "strong")
	if child.Execution() != tools.ExecCode {
		t.Errorf("the child should follow its own model changes: %q", child.Execution())
	}
}
