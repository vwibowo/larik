package agent

import (
	"context"
	"sync"

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

	for i, use := range uses {
		res := llm.Block{Type: llm.BlockToolResult, ID: use.ID, Name: use.Name}
		tool, ok := a.opts.Tools.Get(use.Name)
		switch {
		case ctx.Err() != nil:
			res.Content, res.IsError = "interrupted by user", true
		case !ok:
			res.Content, res.IsError = "unknown tool: "+use.Name, true
		case use.Input == nil:
			res.Content, res.IsError = "INVALID_JSON: the tool input was truncated or malformed; retry the call with complete, valid JSON", true
		default:
			allow, reason := a.authorize(ctx, use, tool, emit)
			if allow {
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
		if queue[start].tool.ReadOnly() {
			for end < len(queue) && queue[end].tool.ReadOnly() {
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
	out := job.tool.Run(ctx, a.env, use.Input)
	res.Content, res.IsError = out.Content, out.IsError
	emit(Event{Kind: EvToolEnd, ToolID: use.ID, ToolName: use.Name, Input: use.Input, Output: out.Content, Display: out.Display, IsError: out.IsError})
	return res
}

// authorize applies permission rules, asking the front end when needed.
func (a *Agent) authorize(ctx context.Context, use llm.Block, tool tools.Tool, emit func(Event)) (bool, string) {
	perms := a.opts.Perms
	if perms == nil {
		return true, ""
	}
	decision, reason := perms.Decide(permission.Call{Tool: use.Name, ReadOnly: tool.ReadOnly(), Input: use.Input})
	switch decision {
	case permission.Allow:
		return true, ""
	case permission.Deny:
		return false, "Permission denied: " + reason
	}

	reply := make(chan PermissionReply, 1)
	rule := permission.SuggestRule(use.Name, use.Input)
	emit(Event{Kind: EvPermission, ToolID: use.ID, ToolName: use.Name, Input: use.Input, SuggestedRule: rule, Reply: reply})
	select {
	case <-ctx.Done():
		return false, "interrupted by user"
	case r := <-reply:
		if !r.Allow {
			msg := "The user denied this tool call."
			if r.Reason != "" {
				msg += " Their feedback: " + r.Reason
			}
			return false, msg
		}
		if r.Always {
			perms.AddAllow(rule)
			if a.opts.OnAllowRule != nil {
				a.opts.OnAllowRule(rule)
			}
		}
		return true, ""
	}
}
