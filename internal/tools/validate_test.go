package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"larik/internal/llm"
)

func readSpec() llm.ToolSpec { return Read{}.Spec() }

func TestValidateAgainstSchemaAcceptsWhatToolsAreSent(t *testing.T) {
	for _, tc := range []struct {
		name, input string
	}{
		{"just the required argument", `{"path":"a.go"}`},
		{"with the optional ones", `{"path":"a.go","offset":1,"limit":50}`},
		// Models commonly send null for an argument they are not using.
		// Go's unmarshal ignores it, so rejecting it here would refuse
		// calls that worked before this check existed.
		{"null for an optional argument", `{"path":"a.go","offset":null,"limit":null}`},
		// A schema that does not forbid extra properties must tolerate
		// them: a tool may accept more than it declares.
		{"an unknown extra property", `{"path":"a.go","encoding":"utf8"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateAgainstSchema(readSpec(), json.RawMessage(tc.input)); err != nil {
				t.Errorf("%s was rejected: %v", tc.input, err)
			}
		})
	}
}

func TestValidateAgainstSchemaRefusesBadArguments(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"a missing required argument", `{}`, "path"},
		{"the required argument mistyped", `{"path":123}`, "path"},
		{"an optional argument mistyped", `{"path":"a.go","offset":"ten"}`, "offset"},
		{"not an object at all", `["a.go"]`, ""},
		{"not JSON", `{path:}`, "valid JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAgainstSchema(readSpec(), json.RawMessage(tc.input))
			if err == nil {
				t.Fatalf("%s should have been refused", tc.input)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should name %q: %v", tc.want, err)
			}
		})
	}
}

// A schema the validator cannot use is not enforced, so the tool stays the
// judge rather than every call being refused.
func TestUnusableSchemasAreNotEnforced(t *testing.T) {
	for _, raw := range []string{"", "not json", "null", `{"$ref":"https://example.com/x.json"}`} {
		spec := llm.ToolSpec{Name: "x", Schema: json.RawMessage(raw)}
		if err := ValidateAgainstSchema(spec, json.RawMessage(`{"whatever":1}`)); err != nil {
			t.Errorf("schema %q should not be enforced: %v", raw, err)
		}
	}
}

// Every built-in tool's schema must compile, or it would silently stop
// being enforced.
func TestEveryBuiltinSchemaCompiles(t *testing.T) {
	for _, tool := range Default().list {
		spec := tool.Spec()
		if len(spec.Schema) == 0 {
			continue
		}
		if resolveSchema(spec.Schema) == nil {
			t.Errorf("%s declares a schema that cannot be compiled: %s", spec.Name, spec.Schema)
		}
	}
}
