package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/tools"
)

func planAgent(t *testing.T, script ...llm.Message) (*Agent, *fakeProvider) {
	t.Helper()
	a, fp, _ := setup(t, permission.ModePlan, script...)
	a.SetTools(tools.Default().With(ExitPlanTool{}))
	return a, fp
}

// resultOf returns the tool result for id in the last request.
func resultOf(fp *fakeProvider, id string) llm.Block {
	for _, m := range fp.requests[len(fp.requests)-1].Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult && b.ID == id {
				return b
			}
		}
	}
	return llm.Block{}
}

func TestExitPlanModeApproved(t *testing.T) {
	a, fp := planAgent(t,
		assistant(toolUse("p1", "exit_plan_mode", `{"plan":"1. Fix the bug\n2. Run the tests"}`)),
		assistant(llm.TextBlock("starting")),
	)
	var asked []Event
	for e := range a.Run(context.Background(), "fix the bug") {
		if e.Kind == EvPermission {
			asked = append(asked, e)
			e.Reply <- PermissionReply{Allow: true, Mode: permission.ModeAcceptEdits}
		}
	}
	if len(asked) != 1 || asked[0].SuggestedRule != "" {
		t.Fatalf("the plan should be put to the user once, with no rule to save: %+v", asked)
	}
	note := fp.requests[0].Messages[0].Text()
	if !strings.Contains(note, "Plan mode is on") || !strings.Contains(note, "Bash is blocked") ||
		!strings.Contains(note, "do not retry") {
		t.Errorf("the model should be told to use read-only tools, not bash: %q", note)
	}
	if strings.Contains(note, "run_code") || strings.Contains(note, "graphify") || strings.Contains(note, "web_") {
		t.Errorf("the note should name only tools the model has, and nothing project-specific: %q", note)
	}
	if got := a.Perms().Mode(); got != permission.ModeAcceptEdits {
		t.Fatalf("mode after approval = %s", got)
	}
	if r := resultOf(fp, "p1"); r.IsError || !strings.Contains(r.Content, "approved") || !strings.Contains(r.Content, "accept-edits") {
		t.Fatalf("result = %+v", r)
	}
}

// netTool stands for a network tool: not read-only, so plan mode asks for it.
type netTool struct{ name string }

func (netTool) ReadOnly() bool { return false }
func (n netTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: n.name, Description: "Reach the web", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (netTool) Run(context.Context, *tools.Env, json.RawMessage) tools.Result {
	return tools.Result{Content: "fetched"}
}

// The note says which network tools still work, so the model doesn't
// assume plan mode cuts it off from the web, and the call asks rather
// than being denied.
func TestPlanModeNoteNamesNetworkTools(t *testing.T) {
	a, fp := planAgent(t,
		assistant(toolUse("w1", "web_search", `{}`)),
		assistant(llm.TextBlock("found it")))
	a.SetTools(tools.Default().With(ExitPlanTool{}, netTool{"web_fetch"}, netTool{"web_search"}))
	evs := drain(a.Run(context.Background(), "research it"), PermissionReply{Allow: true})

	note := fp.requests[0].Messages[0].Text()
	if !strings.Contains(note, " web_fetch and web_search still work; each call asks the user first.") {
		t.Errorf("the note should name the network tools the model has: %q", note)
	}
	if strings.Contains(note, "browser_navigate") {
		t.Errorf("the note should not name a network tool the model lacks: %q", note)
	}
	if asked := permissionEvents(evs); len(asked) != 1 || asked[0].ToolName != "web_search" {
		t.Fatalf("plan mode should ask for web_search: %+v", asked)
	}
	if r := resultOf(fp, "w1"); r.IsError || r.Content != "fetched" {
		t.Fatalf("approved web_search should run in plan mode: %+v", r)
	}
}

func TestCodeExecutionCanPresentPlan(t *testing.T) {
	a, fp := planAgent(t,
		assistant(toolUse("p1", "exit_plan_mode", `{"plan":"Fix the bug"}`)),
		assistant(llm.TextBlock("starting")))
	a.SetExecution(tools.ExecCode)
	evs := drain(a.Run(context.Background(), "plan it"), PermissionReply{Allow: true, Mode: permission.ModeAcceptEdits})
	if got := a.Perms().Mode(); got != permission.ModeAcceptEdits {
		t.Fatalf("code execution could not leave plan mode: %s", got)
	}
	if asked := permissionEvents(evs); len(asked) != 1 || asked[0].ToolName != "exit_plan_mode" {
		t.Fatalf("plan approval events = %+v", asked)
	}
	if declared := declaredNames(fp.requests[0].Tools); declared != codeDeclared+",exit_plan_mode" {
		t.Fatalf("code execution tools = %v", declared)
	}
	if note := fp.requests[0].Messages[0].Text(); !strings.Contains(note, "wrapping bash in run_code does not bypass this") {
		t.Errorf("with run_code, the note should say it can't get around plan mode: %q", note)
	}
}

func TestExitPlanModeRejected(t *testing.T) {
	a, fp := planAgent(t,
		assistant(toolUse("p1", "exit_plan_mode", `{"plan":"rewrite everything"}`)),
		assistant(llm.TextBlock("revising")),
	)
	drain(a.Run(context.Background(), "fix the bug"), PermissionReply{Reason: "smaller change please"})
	if got := a.Perms().Mode(); got != permission.ModePlan {
		t.Fatalf("mode after rejection = %s", got)
	}
	if r := resultOf(fp, "p1"); !r.IsError || !strings.Contains(r.Content, "stays on") || !strings.Contains(r.Content, "smaller change please") {
		t.Fatalf("result = %+v", r)
	}
}

func TestExitPlanModeNeedsAPlan(t *testing.T) {
	a, fp := planAgent(t,
		assistant(toolUse("p1", "exit_plan_mode", `{"plan":"  "}`)),
		assistant(llm.TextBlock("ok")),
	)
	for e := range a.Run(context.Background(), "go") {
		if e.Kind == EvPermission {
			t.Fatal("an empty plan must not be put to the user")
		}
	}
	if r := resultOf(fp, "p1"); !r.IsError || !strings.Contains(r.Content, "needs the plan") {
		t.Fatalf("result = %+v", r)
	}
}

func TestPlanEndedNote(t *testing.T) {
	a, fp := planAgent(t, assistant(llm.TextBlock("a plan")), assistant(llm.TextBlock("ok")), assistant(llm.TextBlock("ok")))
	drain(a.Run(context.Background(), "plan it"), PermissionReply{})
	a.Perms().SetMode(permission.ModeDefault) // the user left plan mode with shift+tab
	drain(a.Run(context.Background(), "now do it"), PermissionReply{})
	last := fp.requests[1].Messages
	if text := last[len(last)-1].Text(); !strings.Contains(text, "Plan mode is off") || strings.Contains(text, "Plan mode is on") {
		t.Fatalf("second prompt: %q", text)
	}
	drain(a.Run(context.Background(), "more"), PermissionReply{})
	last = fp.requests[2].Messages
	if strings.Contains(last[len(last)-1].Text(), "Plan mode") {
		t.Fatal("the ended note is said once")
	}
}

// A subagent shares the parent's plan mode, so it gets a note of its own,
// and a denial points it at its final message, not at exit_plan_mode,
// which it doesn't have.
func TestSubagentInPlanMode(t *testing.T) {
	parent, _, dir := setup(t, permission.ModePlan)
	cp := &fakeProvider{script: []llm.Message{
		assistant(toolUse("e1", "edit", `{"path":"a.txt","old_string":"a","new_string":"b"}`)),
		assistant(llm.TextBlock("a.txt should say b")),
	}}
	child := parent.Spawn(SpawnOptions{Type: "worker", Provider: cp, Model: "m", Tools: tools.Default().With(netTool{"web_fetch"})})
	drain(child.Run(context.Background(), "change a.txt"), PermissionReply{})

	note := cp.requests[0].Messages[0].Text()
	if !strings.Contains(note, "Plan mode is on") || !strings.Contains(note, "report in your final message") ||
		!strings.Contains(note, "web_fetch still work") {
		t.Errorf("the subagent should be told plan mode is on and how to report: %q", note)
	}
	if strings.Contains(note, "exit_plan_mode") {
		t.Errorf("the subagent has no exit_plan_mode: %q", note)
	}
	res := resultOf(cp, "e1")
	if !res.IsError || !strings.HasPrefix(res.Content, "Permission denied: "+permission.PlanModeDenied) ||
		!strings.Contains(res.Content, "web_fetch still work") || !strings.Contains(res.Content, "final message") ||
		strings.Contains(res.Content, "exit_plan_mode") {
		t.Errorf("denial = %q", res.Content)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("plan mode must block the subagent's edit")
	}
}

// The main agent's denial names exit_plan_mode, and only the network
// tools it has.
func TestPlanModeDenialForMainAgent(t *testing.T) {
	a, fp := planAgent(t,
		assistant(toolUse("w1", "write", `{"path":"a.txt","content":"A"}`)),
		assistant(llm.TextBlock("planned")))
	drain(a.Run(context.Background(), "write it"), PermissionReply{})

	res := resultOf(fp, "w1")
	if !res.IsError || !strings.Contains(res.Content, permission.PlanModeDenied) || !strings.Contains(res.Content, "exit_plan_mode") {
		t.Errorf("denial = %q", res.Content)
	}
	if strings.Contains(res.Content, "web_") || strings.Contains(res.Content, "browser_") {
		t.Errorf("the denial should not name network tools the model lacks: %q", res.Content)
	}
}
