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

// A built-in tool is held to the schema the model was given, and the check
// happens before the tool runs, so a bad call has no side effect.
func TestBuiltinToolArgumentsAreCheckedBeforeItRuns(t *testing.T) {
	for _, tc := range []struct {
		name, tool, input, want string
	}{
		{"a missing required argument", "write", `{"path":"out.txt"}`, "content"},
		{"a wrong type", "read", `{"path":"a.go","offset":"ten"}`, "offset"},
		{"the required argument mistyped", "write", `{"path":42,"content":"x"}`, "path"},
		{"an empty object where arguments are required", "write", `{}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, dir := setup(t, permission.ModeYolo,
				assistant(toolUse("t1", tc.tool, tc.input)),
				assistant(llm.TextBlock("ok")))
			drain(a.Run(context.Background(), "go"), PermissionReply{Allow: true})

			res := lastToolResult(t, a)
			if !res.IsError || !strings.Contains(res.Content, "INVALID_ARGUMENTS") {
				t.Fatalf("result = %+v", res)
			}
			// The message points the model at the tool's own contract.
			if !strings.Contains(res.Content, "schema") {
				t.Errorf("message should mention the schema: %q", res.Content)
			}
			if tc.want != "" && !strings.Contains(res.Content, tc.want) {
				t.Errorf("message should name %q: %q", tc.want, res.Content)
			}
			// Nothing was written: the tool never ran.
			if _, err := os.Stat(filepath.Join(dir, "out.txt")); err == nil {
				t.Error("the tool ran despite invalid arguments")
			}
		})
	}
}

// The check is reported as the model's fault, so bench counts it.
func TestSchemaRejectionCountsAsAnInvalidArgumentsFault(t *testing.T) {
	a, _, _ := setup(t, permission.ModeYolo,
		assistant(toolUse("t1", "write", `{"path":"x.txt"}`)),
		assistant(llm.TextBlock("ok")))
	got := faultsOf(drain(a.Run(context.Background(), "go"), PermissionReply{Allow: true}))
	if len(got) != 1 || got[0] != FaultInvalidArguments {
		t.Errorf("faults = %v, want [%s]", got, FaultInvalidArguments)
	}
}

// Valid calls must still run, and a schema that does not forbid extra
// properties must not have them rejected: a tool may accept more than it
// declares, and refusing a call the tool would have handled is the worse
// failure.
func TestValidCallsAndExtraPropertiesStillRun(t *testing.T) {
	for _, input := range []string{
		`{"path":"out.txt","content":"hello"}`,
		`{"path":"out.txt","content":"hello","mode":"0644"}`,
	} {
		a, _, dir := setup(t, permission.ModeYolo,
			assistant(toolUse("t1", "write", input)),
			assistant(llm.TextBlock("ok")))
		drain(a.Run(context.Background(), "go"), PermissionReply{Allow: true})

		if res := lastToolResult(t, a); res.IsError {
			t.Errorf("input %s was rejected: %q", input, res.Content)
		}
		if b, err := os.ReadFile(filepath.Join(dir, "out.txt")); err != nil || string(b) != "hello" {
			t.Errorf("input %s: file = %q, err %v", input, b, err)
		}
	}
}

// looseTool declares a schema but judges arguments itself, which has to win
// over the generic check — a tool knows its own contract best.
type looseTool struct{ ran *bool }

func (looseTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: "loose", Description: "d", Schema: json.RawMessage(
		`{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"]}`)}
}
func (looseTool) ReadOnly() bool                      { return true }
func (looseTool) ValidateInput(json.RawMessage) error { return nil }
func (l looseTool) Run(context.Context, *tools.Env, json.RawMessage) tools.Result {
	*l.ran = true
	return tools.Result{Content: "ran"}
}

func TestAToolThatValidatesItselfKeepsControl(t *testing.T) {
	ran := false
	dir := t.TempDir()
	fp := &fakeProvider{script: []llm.Message{
		assistant(toolUse("t1", "loose", `{"n":"not an integer"}`)),
		assistant(llm.TextBlock("ok"))}}
	a := New(Options{Provider: fp, Model: "m", Cwd: dir,
		Tools: tools.NewRegistry().With(looseTool{&ran}),
		Perms: permission.NewChecker(permission.ModeYolo, permission.Rules{}, dir)})
	drain(a.Run(context.Background(), "go"), PermissionReply{Allow: true})

	if !ran {
		t.Error("the tool's own ValidateInput said yes, so it should have run")
	}
}

// An unusable or absent schema is not enforced, so such a tool behaves as
// it did before this check existed.
func TestAnUnusableSchemaIsNotEnforced(t *testing.T) {
	for _, raw := range []string{"", "not json", `{"$ref":"https://example.com/remote.json"}`} {
		spec := llm.ToolSpec{Name: "x", Schema: json.RawMessage(raw)}
		if err := tools.ValidateAgainstSchema(spec, json.RawMessage(`{"anything":1}`)); err != nil {
			t.Errorf("schema %q should not be enforced, got %v", raw, err)
		}
	}
}
