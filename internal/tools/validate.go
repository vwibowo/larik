package tools

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
	"larik/internal/llm"
)

// resolvedSchemas caches compiled schemas by their JSON. A tool's schema is
// fixed for the life of the process and the same few are checked on every
// call, so compiling once matters.
var resolvedSchemas sync.Map // string -> *jsonschema.Resolved (nil when unusable)

// ValidateAgainstSchema checks input against the schema a tool declares, so
// a call with a missing or mistyped argument is refused with the tool's own
// contract to point at rather than failing somewhere inside the tool.
//
// A schema that cannot be compiled is not enforced, and neither is an empty
// one: a tool is free to accept more than it declares, and refusing a call
// a tool would have handled is worse than letting the tool judge it.
func ValidateAgainstSchema(spec llm.ToolSpec, input json.RawMessage) error {
	r := resolveSchema(spec.Schema)
	if r == nil {
		return nil
	}
	var args any
	if err := json.Unmarshal(input, &args); err != nil {
		return fmt.Errorf("arguments are not valid JSON: %w", err)
	}
	if err := r.Validate(dropNulls(args)); err != nil {
		return fmt.Errorf("arguments do not match the tool's schema: %w", err)
	}
	return nil
}

// dropNulls removes properties whose value is null, because that is what
// unmarshalling into a tool's own struct does with them: models routinely
// send null for an argument they are not using, and a schema that types the
// argument would otherwise refuse a call the tool would have handled. A null
// where the schema requires a value still fails, as a missing one.
func dropNulls(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := make(map[string]any, len(m))
	for k, val := range m {
		if val == nil {
			continue
		}
		out[k] = dropNulls(val)
	}
	return out
}

// resolveSchema compiles schema, returning nil when it cannot be used for
// validation.
func resolveSchema(raw json.RawMessage) *jsonschema.Resolved {
	if len(raw) == 0 {
		return nil
	}
	if v, ok := resolvedSchemas.Load(string(raw)); ok {
		r, _ := v.(*jsonschema.Resolved)
		return r
	}
	r := compile(raw)
	resolvedSchemas.Store(string(raw), r)
	return r
}

func compile(raw json.RawMessage) *jsonschema.Resolved {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return nil // not an object: a literal null or a bare value
	}
	// The validator knows draft-07 and 2020-12 and treats an unmarked
	// schema as the latter, so a declared dialect it does not know would
	// only get in the way.
	delete(m, "$schema")
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	var s jsonschema.Schema
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	r, err := s.Resolve(nil)
	if err != nil {
		return nil
	}
	return r
}
