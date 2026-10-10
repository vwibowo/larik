package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/tools"
)

// ExitPlanTool presents a plan for approval and leaves plan mode. The
// approval is its permission prompt: permission.Decide always asks for it
// in plan mode, and authorize applies the mode the reply picks. By the
// time Run executes, the user has said yes.
type ExitPlanTool struct{}

// ReadOnly: it changes nothing itself, and must be callable in plan mode.
func (ExitPlanTool) ReadOnly() bool { return true }

func (ExitPlanTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: permission.ExitPlanTool,
		Description: "In plan mode, present your finished plan to the user for approval. If they approve, plan mode ends and you can " +
			"make the changes; if not, you get their feedback and stay in plan mode to revise the plan. Use it only when the task needs " +
			"changes you have planned (not for research or questions, which you just answer), and only once the plan is complete: " +
			"which files change and how, in what order, and how you will check the result. Write the plan in Markdown, concise but specific.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"plan":{"type":"string","description":"The plan, in Markdown"}},
			"required":["plan"]}`),
	}
}

func (ExitPlanTool) Run(ctx context.Context, _ *tools.Env, _ json.RawMessage) tools.Result {
	mode := permission.ModeDefault
	if a, _, ok := FromContext(ctx); ok && a.opts.Perms != nil {
		mode = a.opts.Perms.Mode()
	}
	if mode == permission.ModePlan { // only reachable if a hook allowed it
		return tools.Result{Content: "Plan mode is still on; the plan wasn't approved.", IsError: true}
	}
	return tools.Result{Content: fmt.Sprintf("The user approved the plan. Plan mode is off; the permission mode is now %s. "+
		"Carry out the plan, tracking its steps with todo_write if there are several.", mode)}
}

// PlanOf returns the plan text of an exit_plan_mode input.
func PlanOf(input json.RawMessage) string {
	var in struct {
		Plan string `json:"plan"`
	}
	_ = json.Unmarshal(input, &in)
	return strings.TrimSpace(in.Plan)
}

// Notes that tell the model about plan mode, prepended to user messages
// rather than the system prompt so the cached prefix stays the same.
const (
	planModeNote = "Plan mode is on: explore and plan with read-only tools, and don't change anything. " +
		"Bash is blocked even for commands that look read-only%s. " +
		"Use read, grep, and glob instead, and do not retry a denied command. When the plan is ready, present it with " +
		"exit_plan_mode for the user's approval; if the request is a question rather than a change, just answer it."
	// planCodeClause joins planModeNote when the model has run_code.
	planCodeClause = "; wrapping bash in run_code does not bypass this"
	planEndedNote  = "Plan mode is off now: you may edit files and run commands again, subject to the usual permissions."
)

// planNotesLocked adds the plan-mode note for the next prompt. a.mu must be held.
func (a *Agent) planNotesLocked() {
	if a.opts.Perms == nil || a.opts.Subagent != "" {
		return
	}
	switch {
	case a.opts.Perms.Mode() == permission.ModePlan:
		clause := ""
		if reg := a.activeLocked(); reg != nil {
			if _, ok := reg.Get(tools.CodeToolName); ok {
				clause = planCodeClause
			}
		}
		a.notes = append(a.notes, fmt.Sprintf(planModeNote, clause))
		a.planNoted = true
	case a.planNoted:
		a.notes = append(a.notes, planEndedNote)
		a.planNoted = false
	}
}

// approvePlan applies an approved exit_plan_mode reply: plan mode ends in
// the mode the user picked (default when none, or plan itself).
func (a *Agent) approvePlan(mode permission.Mode) {
	if mode == "" || mode == permission.ModePlan {
		mode = permission.ModeDefault
	}
	a.opts.Perms.SetMode(mode)
	a.mu.Lock()
	a.planNoted = false // the tool result says so
	a.mu.Unlock()
}

// AddNote queues text for the model to see with the next prompt, as a
// system note: something that changed outside the conversation.
func (a *Agent) AddNote(text string) {
	a.mu.Lock()
	a.notes = append(a.notes, text)
	a.mu.Unlock()
}
