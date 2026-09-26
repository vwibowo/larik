package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/tools"
)

const (
	WaitToolName = "task_wait"
	StopToolName = "task_stop"

	defaultWait = 10 * time.Minute
)

// WaitTool blocks until background tasks finish and returns their results.
type WaitTool struct{}

func (WaitTool) ReadOnly() bool { return true }

func (WaitTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: WaitToolName,
		Description: "Wait for background tasks started with task(run_in_background: true) and return their results. " +
			"With no ids it waits for every unfinished task. Only wait when you can't make progress without the result; otherwise keep working and results arrive on their own.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"ids":{"type":"array","items":{"type":"string"},"description":"Task ids such as bg-1; omit to wait for all"},
			"timeout_seconds":{"type":"integer","description":"Give up waiting after this long (default 600)"}}}`),
	}
}

func (WaitTool) Run(ctx context.Context, _ *tools.Env, input json.RawMessage) tools.Result {
	var in struct {
		IDs     []string `json:"ids"`
		Timeout int      `json:"timeout_seconds"`
	}
	if len(input) > 0 && json.Unmarshal(input, &in) != nil {
		return tools.Result{Content: "INVALID_JSON", IsError: true}
	}
	parent, _, ok := agent.FromContext(ctx)
	if !ok {
		return tools.Result{Content: "task_wait: no agent in context", IsError: true}
	}
	timeout := defaultWait
	if in.Timeout > 0 {
		timeout = time.Duration(in.Timeout) * time.Second
	}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tasks, err := parent.WaitBackground(wctx, in.IDs)
	if err != nil && len(tasks) == 0 {
		return tools.Result{Content: err.Error(), IsError: true}
	}
	if len(tasks) == 0 {
		return tools.Result{Content: "No background tasks to wait for."}
	}
	var b strings.Builder
	for i, t := range tasks {
		if i > 0 {
			b.WriteString("\n\n")
		}
		if t.Status == agent.BgRunning {
			fmt.Fprintf(&b, "<task id=%q agent=%q status=\"still running\" elapsed=%q/>", t.ID, t.Label, time.Since(t.Started).Round(time.Second))
			continue
		}
		fmt.Fprintf(&b, "<task id=%q agent=%q status=%q>\n%s\n</task>", t.ID, t.Label, t.Status, t.Result)
	}
	if ctx.Err() != nil {
		b.WriteString("\n\n(wait interrupted by user)")
	}
	return tools.Result{Content: tools.Truncate(b.String(), tools.MaxOutputBytes*2)}
}

// StopTool cancels a background task.
type StopTool struct{}

func (StopTool) ReadOnly() bool { return true }

func (StopTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        StopToolName,
		Description: "Cancel a running background task by id (e.g. when its result is no longer needed).",
		Schema:      json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
	}
}

func (StopTool) Run(ctx context.Context, _ *tools.Env, input json.RawMessage) tools.Result {
	var in struct {
		ID string `json:"id"`
	}
	if len(input) == 0 || json.Unmarshal(input, &in) != nil || in.ID == "" {
		return tools.Result{Content: "INVALID_JSON: expected {\"id\": ...}", IsError: true}
	}
	parent, _, ok := agent.FromContext(ctx)
	if !ok {
		return tools.Result{Content: "task_stop: no agent in context", IsError: true}
	}
	if err := parent.StopBackground(in.ID); err != nil {
		return tools.Result{Content: err.Error(), IsError: true}
	}
	return tools.Result{Content: "Stopped " + in.ID + "."}
}
