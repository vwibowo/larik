package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"larik/internal/llm"
	"larik/internal/permission"
)

// checkWriteDestination checks a destination the language server named
// after the call was authorized. Explicit denials always apply. A user's or
// hook's approval covers the whole workspace operation (the prompt says it
// may change other files), but a rule or mode allowed only the path it
// matched, so a destination it would ask about is refused.
func (a *Agent) checkWriteDestination(use llm.Block, path string, granted bool) error {
	if a.opts.Perms == nil {
		return nil
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal(use.Input, &input); err != nil {
		return err
	}
	input["path"], _ = json.Marshal(path)
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	decision, reason := a.opts.Perms.Decide(permission.Call{Tool: use.Name, Input: raw})
	switch {
	case decision == permission.Deny:
		return fmt.Errorf("permission denied: %s", reason)
	case decision == permission.Ask && !granted:
		name := path
		if rel, err := filepath.Rel(a.opts.Cwd, path); err == nil && filepath.IsLocal(rel) {
			name = rel
		}
		return fmt.Errorf("permission needed: the action would also change %s, which the rule that allowed it does not cover; make that change with edit, which asks the user", name)
	}
	return nil
}
