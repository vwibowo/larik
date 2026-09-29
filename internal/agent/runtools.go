package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"larik/internal/hooks"
	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/tools"
)

type approved struct {
	idx  int
	use  llm.Block
	tool tools.Tool
}

// runTools checks permissions for every call (in order, since asking is
// interactive), then executes approved calls. Consecutive read-only calls
// run in parallel; anything that writes runs alone. Results come back in
// the original order, one per call, as a single user message.
func (a *Agent) runTools(ctx context.Context, uses []llm.Block, emit func(Event)) []llm.Block {
	results := make([]llm.Block, len(uses))
	var queue []approved
	registry := a.Tools()

	for i, use := range uses {
		res := llm.Block{Type: llm.BlockToolResult, ID: use.ID, Name: use.Name}
		tool, ok := registry.Get(use.Name)
		switch {
		case ctx.Err() != nil:
			res.Content, res.IsError = "interrupted by user", true
		case !ok:
			res.Content, res.IsError = "unknown tool: "+use.Name, true
		case use.Input == nil:
			res.Content, res.IsError = "INVALID_JSON: the tool input was truncated or malformed; retry the call with complete, valid JSON", true
		default:
			allow, reason, input := a.authorize(ctx, use, tool, emit)
			if allow {
				use.Input = input
				queue = append(queue, approved{i, use, tool})
				continue
			}
			res.Content, res.IsError = reason, true
		}
		results[i] = res
		emit(Event{Kind: EvToolEnd, ToolID: use.ID, ToolName: use.Name, Input: use.Input, Output: res.Content, IsError: true})
	}

	for start := 0; start < len(queue); {
		end := start + 1
		if tools.Parallel(queue[start].tool) {
			for end < len(queue) && tools.Parallel(queue[end].tool) {
				end++
			}
		}
		var wg sync.WaitGroup
		for _, job := range queue[start:end] {
			wg.Add(1)
			go func(job approved) {
				defer wg.Done()
				results[job.idx] = a.execute(ctx, job, emit)
			}(job)
		}
		wg.Wait()
		start = end
	}
	return results
}

func (a *Agent) execute(ctx context.Context, job approved, emit func(Event)) llm.Block {
	use := job.use
	res := llm.Block{Type: llm.BlockToolResult, ID: use.ID, Name: use.Name}
	if ctx.Err() != nil {
		res.Content, res.IsError = "interrupted by user", true
		return res
	}
	emit(Event{Kind: EvToolStart, ToolID: use.ID, ToolName: use.Name, Input: use.Input})
	out := job.tool.Run(tools.WithCallID(withRun(ctx, a, emit), use.ID), a.env, use.Input)
	res.Content, res.IsError, res.Images = out.Content, out.IsError, out.Images
	emit(Event{Kind: EvToolEnd, ToolID: use.ID, ToolName: use.Name, Input: use.Input, Output: out.Content, Display: out.Display, IsError: out.IsError})

	post := a.runHook(ctx, emit, hooks.Input{
		HookEventName: hooks.PostToolUse, ToolName: use.Name, ToolInput: use.Input, ToolUseID: use.ID,
		ToolResponse: &hooks.ToolResponse{Output: out.Content, IsError: out.IsError},
	}, use.Name)
	if post.Block && post.Reason != "" {
		res.Content += "\n\n" + hookFeedback("PostToolUse", post.Reason)
	}
	if len(post.Context) > 0 {
		res.Content += "\n\n" + hookContext("PostToolUse", post.Context)
	}
	if post.Halt {
		a.requestHalt(post.HaltReason)
	}
	return res
}

// authorize runs PreToolUse hooks and permission rules, asking the front
// end when needed. It returns the (possibly hook-rewritten) input to run.
// Order: hook deny > rule deny > hook allow/ask > rules/mode > prompt.
func (a *Agent) authorize(ctx context.Context, use llm.Block, tool tools.Tool, emit func(Event)) (bool, string, json.RawMessage) {
	input := use.Input
	pre := a.runHook(ctx, emit, hooks.Input{HookEventName: hooks.PreToolUse, ToolName: use.Name, ToolInput: input, ToolUseID: use.ID}, use.Name)
	if pre.Halt {
		a.requestHalt(pre.HaltReason)
		return false, "Stopped by a PreToolUse hook: " + pre.HaltReason, input
	}
	if pre.Block || pre.Permission == "deny" {
		return false, "Blocked by PreToolUse hook: " + pre.Reason, input
	}
	if pre.UpdatedInput != nil {
		input = pre.UpdatedInput
	}

	perms := a.opts.Perms
	if perms == nil {
		return true, "", input
	}
	decision, reason := perms.Decide(permission.Call{Tool: use.Name, ReadOnly: tool.ReadOnly(), Input: input})
	tr := a.tracer()
	record := func(answer, why string, asked time.Time) {
		tr.Permission(use.ID, use.Name, permission.SuggestRule(use.Name, input), answer, why, asked)
	}
	switch {
	case decision == permission.Deny:
		record("deny (rule)", reason, time.Now())
		return false, "Permission denied: " + reason, input
	case pre.Permission == "allow":
		record("allow (hook)", pre.Reason, time.Now())
		return true, "", input
	case pre.Permission == "ask":
		decision = permission.Ask
	}
	if decision == permission.Allow {
		record("allow (rules or mode)", reason, time.Now())
		return true, "", input
	}

	exitPlan := use.Name == permission.ExitPlanTool
	if exitPlan && PlanOf(input) == "" {
		return false, "exit_plan_mode needs the plan: pass it as plan, in Markdown.", input
	}
	reply := make(chan PermissionReply, 1)
	rule := permission.SuggestRule(use.Name, input)
	a.notify("Larik needs your permission to use " + use.Name)
	asked := time.Now()
	emit(Event{Kind: EvPermission, ToolID: use.ID, ToolName: use.Name, Input: input, SuggestedRule: rule, Cwd: a.opts.Cwd, Reply: reply})
	select {
	case <-ctx.Done():
		record("interrupted", "", asked)
		return false, "interrupted by user", input
	case r := <-reply:
		switch {
		case exitPlan && r.Allow:
			record("plan approved", "continue in "+string(r.Mode), asked)
		case exitPlan:
			record("plan not approved", r.Reason, asked)
		case !r.Allow:
			record("deny", r.Reason, asked)
		case r.Always:
			record("always allow", "", asked)
		default:
			record("allow", "", asked)
		}
		if exitPlan {
			if !r.Allow {
				msg := "The user didn't approve the plan, so plan mode stays on. Revise the plan"
				if r.Reason != "" {
					return false, msg + " with their feedback: " + r.Reason, input
				}
				return false, msg + ", or ask them what to change.", input
			}
			a.approvePlan(r.Mode)
			return true, "", input
		}
		if !r.Allow {
			msg := "The user denied this tool call."
			if r.Reason != "" {
				msg += " Their feedback: " + r.Reason
			}
			return false, msg, input
		}
		if r.Always {
			perms.AddAllow(rule)
			var err error
			if a.opts.OnAllowRule != nil {
				err = a.opts.OnAllowRule(rule)
			} else {
				err = errors.New("approval persistence is unavailable")
			}
			if err != nil {
				emit(Event{Kind: EvNotice, Text: "Could not save always-allow rule " + rule + ": " + err.Error() + ". It applies only to this session."})
			}
			if r.Persisted != nil {
				select {
				case r.Persisted <- err:
				default:
				}
			}
		}
		return true, "", input
	}
}
