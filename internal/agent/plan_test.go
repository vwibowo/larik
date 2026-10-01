package agent

import (
	"context"
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
	if !strings.Contains(fp.requests[0].Messages[0].Text(), "Plan mode is on") {
		t.Error("the model should be told plan mode is on")
	}
	if got := a.Perms().Mode(); got != permission.ModeAcceptEdits {
		t.Fatalf("mode after approval = %s", got)
	}
	if r := resultOf(fp, "p1"); r.IsError || !strings.Contains(r.Content, "approved") || !strings.Contains(r.Content, "accept-edits") {
		t.Fatalf("result = %+v", r)
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
	var declared []string
	for _, spec := range fp.requests[0].Tools {
		declared = append(declared, spec.Name)
	}
	if strings.Join(declared, ",") != "run_code,exit_plan_mode" {
		t.Fatalf("code execution tools = %v", declared)
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
