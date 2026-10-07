package agent

import (
	"context"
	"testing"

	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/tools"
)

// faultsOf collects the fault on every EvToolEnd, so a test can see what the
// model was judged to have got wrong.
func faultsOf(evs []Event) []ToolFault {
	var out []ToolFault
	for _, e := range evs {
		if e.Kind == EvToolEnd {
			out = append(out, e.Fault)
		}
	}
	return out
}

func TestToolEndReportsHowTheModelMalformedACall(t *testing.T) {
	truncated := toolUse("t2", "read", "")
	truncated.Input = nil

	for _, tc := range []struct {
		name string
		call llm.Block
		want ToolFault
	}{
		{"a tool that does not exist", toolUse("t1", "no_such_tool", `{}`), FaultUnknownTool},
		{"arguments cut off mid-stream", truncated, FaultInvalidJSON},
		// With no deferred tools registered, call_tool is itself absent,
		// so naming it is an unknown tool rather than a bad wrapper.
		{"the wrapper where it is not offered", toolUse("t3", tools.CallToolName, `{"tool":"x","arguments":{}}`), FaultUnknownTool},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := setup(t, permission.ModeYolo, assistant(tc.call), assistant(llm.TextBlock("ok")))
			got := faultsOf(drain(a.Run(context.Background(), "go"), PermissionReply{Allow: true}))
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("faults = %v, want [%s]", got, tc.want)
			}
		})
	}
}

// Where call_tool is offered, a wrapper naming a tool that is not there is
// the wrapper's own fault, distinct from a bad direct call.
func TestWrapperNamingNoUsableToolIsItsOwnFault(t *testing.T) {
	var ran []string
	dir := t.TempDir()
	for _, tc := range []struct {
		name, call string
	}{
		{"an inner tool that does not exist", `{"tool":"nope","arguments":{}}`},
		{"a wrapper shape the model got wrong", `{"arguments":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fp := &fakeProvider{script: []llm.Message{
				assistant(toolUse("1", tools.CallToolName, tc.call)), assistant(llm.TextBlock("ok"))}}
			a := New(Options{Provider: fp, Model: "m", Cwd: dir,
				Tools: tools.NewRegistry().Defer(strictTool{remoteTool{&ran}}),
				Perms: permission.NewChecker(permission.ModeYolo, permission.Rules{}, dir)})
			got := faultsOf(drain(a.Run(context.Background(), "go"), PermissionReply{Allow: true}))
			if len(got) != 1 || got[0] != FaultBadCallTool {
				t.Errorf("faults = %v, want [%s]", got, FaultBadCallTool)
			}
		})
	}
	if len(ran) != 0 {
		t.Errorf("no tool should have run, ran=%v", ran)
	}
}

func TestSchemaRejectionIsAnInvalidArgumentsFault(t *testing.T) {
	var ran []string
	dir := t.TempDir()
	fp := &fakeProvider{script: []llm.Message{
		assistant(toolUse("1", tools.CallToolName, `{"tool":"mcp__tracker__create_issue","arguments":{"name":"Bug"}}`)),
		assistant(llm.TextBlock("ok"))}}
	a := New(Options{Provider: fp, Model: "m", Cwd: dir,
		Tools: tools.NewRegistry().Defer(strictTool{remoteTool{&ran}}),
		Perms: permission.NewChecker(permission.ModeYolo, permission.Rules{}, dir)})
	got := faultsOf(drain(a.Run(context.Background(), "file it"), PermissionReply{Allow: true}))
	if len(got) != 1 || got[0] != FaultInvalidArguments {
		t.Errorf("faults = %v, want [%s]", got, FaultInvalidArguments)
	}
}

// A call the tool actually ran is not the model's fault, however it ended,
// and neither is a refusal: counting faults has to measure the model.
func TestARunOrRefusedCallCarriesNoFault(t *testing.T) {
	a, _, _ := setup(t, permission.ModeYolo,
		assistant(toolUse("t1", "glob", `{"pattern":"*.none"}`)),
		assistant(llm.TextBlock("ok")))
	for _, f := range faultsOf(drain(a.Run(context.Background(), "go"), PermissionReply{})) {
		if f != "" {
			t.Errorf("a tool that ran reported fault %q", f)
		}
	}

	// Plan mode refuses an edit without asking; that is policy, not form.
	b, _, _ := setup(t, permission.ModePlan,
		assistant(toolUse("t1", "edit", `{"path":"a","old_string":"x","new_string":"y"}`)),
		assistant(llm.TextBlock("ok")))
	for _, f := range faultsOf(drain(b.Run(context.Background(), "go"), PermissionReply{})) {
		if f != "" {
			t.Errorf("a refused call reported fault %q", f)
		}
	}
}
