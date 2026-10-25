package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/tools"
)

// autoClockTool checks that authorization waits have ended before any tool
// execution is charged to the script clock.
type autoClockTool struct{ run func() }

func (autoClockTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: "auto_clock_test", Description: "test clock accounting", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (autoClockTool) ReadOnly() bool { return false }
func (t autoClockTool) Run(context.Context, *tools.Env, json.RawMessage) tools.Result {
	t.run()
	return tools.Result{Content: "ran"}
}

func TestAutoClockWaitAccounting(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mode        permission.Mode
		verdict     AutoVerdict
		err         error
		reply       PermissionReply
		cancel      bool
		cancelHuman bool
		wantRun     bool
		wantReason  string
		wantSteps   []string
	}{
		{name: "allow", mode: permission.ModeAuto, verdict: AutoVerdict{Allow: true}, wantRun: true, wantSteps: []string{"pause", "classify", "resume", "run"}},
		{name: "reject then allow", mode: permission.ModeAuto, verdict: AutoVerdict{Reason: "needs review"}, reply: PermissionReply{Allow: true}, wantRun: true, wantReason: "needs review", wantSteps: []string{"pause", "classify", "resume", "pause", "human", "resume", "run"}},
		{name: "error then deny", mode: permission.ModeAuto, err: errors.New("offline"), reply: PermissionReply{Reason: "do not run"}, wantReason: "automatic check failed (offline)", wantSteps: []string{"pause", "classify", "resume", "pause", "human", "resume"}},
		{name: "reject then deny", mode: permission.ModeAuto, verdict: AutoVerdict{Reason: "needs review"}, wantReason: "needs review", wantSteps: []string{"pause", "classify", "resume", "pause", "human", "resume"}},
		{name: "error then allow", mode: permission.ModeAuto, err: errors.New("offline"), reply: PermissionReply{Allow: true}, wantRun: true, wantReason: "automatic check failed (offline)", wantSteps: []string{"pause", "classify", "resume", "pause", "human", "resume", "run"}},
		{name: "cancel classifier", mode: permission.ModeAuto, cancel: true, verdict: AutoVerdict{Allow: true}, wantSteps: []string{"pause", "classify", "resume"}},
		{name: "cancel human followup", mode: permission.ModeAuto, verdict: AutoVerdict{Reason: "needs review"}, cancelHuman: true, wantReason: "needs review", wantSteps: []string{"pause", "classify", "resume", "pause", "human", "resume"}},
		{name: "standalone human wait", mode: permission.ModeDefault, reply: PermissionReply{Allow: true}, wantRun: true, wantSteps: []string{"pause", "human", "resume", "run"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := setup(t, tc.mode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			paused := false
			var steps []string
			ctx = tools.WithUserWait(ctx, func() func() {
				if paused {
					t.Fatal("nested wait")
				}
				paused = true
				steps = append(steps, "pause")
				return func() {
					if !paused {
						t.Fatal("unbalanced resume")
					}
					paused = false
					steps = append(steps, "resume")
				}
			})
			a.SetAutoApprover(func(ctx context.Context, _ AutoCall) (AutoVerdict, error) {
				if !paused {
					t.Error("classifier consumed script execution time")
				}
				steps = append(steps, "classify")
				if tc.cancel {
					cancel()
				}
				return tc.verdict, tc.err
			})
			ran := false
			tool := autoClockTool{run: func() {
				if paused {
					t.Error("tool ran with the script clock paused")
				}
				ran = true
				steps = append(steps, "run")
			}}
			registry := tools.Default().With(tool)
			caller := toolCaller{a: a, tools: registry, parent: "script", emit: func(e Event) {
				if e.Kind != EvPermission {
					return
				}
				if !paused {
					t.Error("human wait consumed script execution time")
				}
				if !strings.Contains(e.AutoReason, tc.wantReason) {
					t.Errorf("auto reason = %q, want %q", e.AutoReason, tc.wantReason)
				}
				steps = append(steps, "human")
				if tc.cancelHuman {
					cancel()
					return
				}
				e.Reply <- tc.reply
			}}
			res := caller.CallTool(ctx, tool.Spec().Name, json.RawMessage(`{}`))
			if ran != tc.wantRun || res.IsError == tc.wantRun {
				t.Errorf("ran = %v, result = %+v", ran, res)
			}
			if (tc.cancel || tc.cancelHuman) && (!errors.Is(ctx.Err(), context.Canceled) || res.Content != "interrupted by user") {
				t.Errorf("cancellation swallowed: %v, %+v", ctx.Err(), res)
			}
			if paused {
				t.Error("clock left paused")
			}
			if !reflect.DeepEqual(steps, tc.wantSteps) {
				t.Errorf("steps = %v, want %v", steps, tc.wantSteps)
			}
		})
	}
}

func TestAutoClockPrefetchedWait(t *testing.T) {
	for _, state := range []string{"pending", "ready", "canceled"} {
		t.Run(state, func(t *testing.T) {
			a, _, _ := setup(t, permission.ModeAuto)
			a.SetAutoApprover(func(context.Context, AutoCall) (AutoVerdict, error) {
				t.Error("prefetched verdict should be used")
				return AutoVerdict{}, errors.New("unexpected classifier")
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pending := &autoPending{done: make(chan struct{}), verdict: AutoVerdict{Allow: true}}
			if state == "ready" {
				close(pending.done)
			}
			paused, pauses, resumes := false, 0, 0
			ctx = tools.WithUserWait(ctx, func() func() {
				paused = true
				pauses++
				// Deterministically make the outstanding verdict available only once
				// the clock has stopped, or cancel while awaiting that verdict.
				if state == "pending" {
					close(pending.done)
				}
				if state == "canceled" {
					cancel()
				}
				return func() { paused = false; resumes++ }
			})
			ran := false
			tool := autoClockTool{run: func() {
				if paused {
					t.Error("prefetched authorization left clock paused during tool execution")
				}
				ran = true
			}}
			use := toolUse("script.1", tool.Spec().Name, `{}`)
			auto := autoVerdicts{use.Name + "\x00" + string(use.Input): pending}
			emit := func(e Event) {
				if e.Kind == EvPermission {
					t.Error("unexpected human prompt")
					e.Reply <- PermissionReply{}
				}
			}
			job, res, ok := a.prepare(ctx, use, tools.Default().With(tool), auto, emit)
			if ok {
				res = a.execute(ctx, job, emit)
			}
			if paused || pauses != 1 || resumes != 1 {
				t.Errorf("paused=%v, pauses=%d, resumes=%d", paused, pauses, resumes)
			}
			if state == "canceled" {
				if ok || ran || !res.IsError || res.Content != "interrupted by user" {
					t.Errorf("cancellation swallowed: ok=%v, ran=%v, result=%+v", ok, ran, res)
				}
			} else if !ok || !ran || res.IsError {
				t.Errorf("prefetched approval failed: ok=%v, ran=%v, result=%+v", ok, ran, res)
			}
		})
	}
}
