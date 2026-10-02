package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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
	// name is the tool the model called, for the result block: it differs
	// from use.Name when the call went through call_tool.
	name string
}

// runTools checks permissions for every call (in order, since asking is
// interactive), then executes approved calls. Consecutive read-only calls
// run in parallel; anything that writes runs alone. Results come back in
// the original order, one per call, as a single user message.
func (a *Agent) runTools(ctx context.Context, uses []llm.Block, emit func(Event)) []llm.Block {
	results := make([]llm.Block, len(uses))
	var queue []approved
	registry := a.activeTools()
	auto := a.prefetchAuto(ctx, uses, registry)

	for i, use := range uses {
		job, res, ok := a.prepare(ctx, use, registry, auto, emit)
		if ok {
			job.idx = i
			queue = append(queue, job)
			continue
		}
		results[i] = res
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
				res := a.execute(ctx, job, emit)
				res.Name = job.name
				results[job.idx] = res
			}(job)
		}
		wg.Wait()
		start = end
	}
	return results
}

// prepare resolves a call to its tool and authorizes it. When the call
// may not run, ok is false and res is the error result to return. auto
// holds auto-mode verdicts started ahead for the batch; it may be nil.
func (a *Agent) prepare(ctx context.Context, use llm.Block, registry *tools.Registry, auto autoVerdicts, emit func(Event)) (job approved, res llm.Block, ok bool) {
	// The result must carry the name the model called, which providers
	// match it by, even when call_tool stands for another tool.
	called := use.Name
	res = llm.Block{Type: llm.BlockToolResult, ID: use.ID, Name: called}
	tool, found := registry.Get(use.Name)
	// call_tool runs a deferred tool. From here on the call is that
	// tool's: permission rules, hooks, auto mode and the events all
	// see its name and arguments, not the wrapper's.
	var unwrapErr error
	if found && use.Name == tools.CallToolName && use.Input != nil && ctx.Err() == nil {
		var inner tools.Tool
		var args json.RawMessage
		if inner, args, unwrapErr = registry.ResolveCall(use.Input); unwrapErr == nil {
			tool, use.Name, use.Input = inner, inner.Spec().Name, args
		}
	}
	switch {
	case ctx.Err() != nil:
		res.Content, res.IsError = "interrupted by user", true
	case !found:
		res.Content, res.IsError = "unknown tool: "+use.Name, true
	case use.Input == nil:
		res.Content, res.IsError = "INVALID_JSON: the tool input was truncated or malformed; retry the call with complete, valid JSON", true
	case unwrapErr != nil:
		res.Content, res.IsError = unwrapErr.Error(), true
	case use.Name == tools.CodeToolName || use.Name == tools.WriteFilesToolName:
		// Wrappers delegate their actions through the same authorization
		// path as direct tool calls (see toolCaller).
		return approved{use: use, tool: tool, name: called}, res, true
	default:
		allow, reason, input := a.authorize(ctx, use, tool, auto, emit)
		if allow {
			use.Input = input
			return approved{use: use, tool: tool, name: called}, res, true
		}
		res.Content, res.IsError = reason, true
	}
	emit(Event{Kind: EvToolEnd, ToolID: use.ID, ToolName: use.Name, Input: use.Input, Output: res.Content, IsError: true})
	return approved{}, res, false
}

// toolCaller runs delegated calls from run_code and write_files one at a time,
// with the same permissions, hooks, checkpoints and events as direct calls.
// Their ids extend the parent tool call's id.
type toolCaller struct {
	a      *Agent
	emit   func(Event)
	parent string
	tools  *tools.Registry
	n      atomic.Int64
}

func (c *toolCaller) CallTool(ctx context.Context, name string, input json.RawMessage) tools.Result {
	use := llm.Block{Type: llm.BlockToolUse, ID: fmt.Sprintf("%s.%d", c.parent, c.n.Add(1)), Name: name, Input: input}
	job, res, ok := c.a.prepare(ctx, use, c.tools, nil, c.emit)
	if ok {
		res = c.a.execute(ctx, job, c.emit)
	}
	return tools.Result{Content: res.Content, IsError: res.IsError, Images: res.Images}
}

func (a *Agent) execute(ctx context.Context, job approved, emit func(Event)) llm.Block {
	use := job.use
	res := llm.Block{Type: llm.BlockToolResult, ID: use.ID, Name: use.Name}
	if ctx.Err() != nil {
		res.Content, res.IsError = "interrupted by user", true
		return res
	}
	emit(Event{Kind: EvToolStart, ToolID: use.ID, ToolName: use.Name, Input: use.Input})
	runCtx := tools.WithOwner(tools.WithCallID(withRun(ctx, a, emit), use.ID), a.owner)
	if use.Name == tools.CodeToolName || use.Name == tools.WriteFilesToolName {
		runCtx = tools.WithCaller(runCtx, &toolCaller{a: a, emit: emit, parent: use.ID, tools: a.Tools()})
	}
	out := job.tool.Run(runCtx, a.env, use.Input)
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
func (a *Agent) authorize(ctx context.Context, use llm.Block, tool tools.Tool, auto autoVerdicts, emit func(Event)) (bool, string, json.RawMessage) {
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
	// Auto mode: a model judges the call first, and only what it doesn't
	// approve reaches the user. A hook that said "ask" wants the user, and
	// a plan is always the user's to approve.
	autoReason := ""
	if perms.Mode() == permission.ModeAuto && pre.Permission != "ask" && !exitPlan {
		started := time.Now()
		verdict, err := auto.get(ctx, a, use.Name, input)
		switch {
		case ctx.Err() != nil:
			return false, "interrupted by user", input
		case err != nil:
			autoReason = "the automatic check failed (" + err.Error() + ")"
		case verdict.Allow:
			record("allow (auto)", verdict.Reason, started)
			return true, "", input
		default:
			autoReason = verdict.Reason
		}
	}
	reply := make(chan PermissionReply, 1)
	rule := permission.SuggestRule(use.Name, input)
	a.notify("Larik needs your permission to use " + use.Name)
	asked := time.Now()
	emit(Event{Kind: EvPermission, ToolID: use.ID, ToolName: use.Name, Input: input, SuggestedRule: rule, Cwd: a.opts.Cwd, AutoReason: autoReason, Reply: reply})
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
