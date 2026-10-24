package agent

import (
	"encoding/json"
	"fmt"

	"larik/internal/llm"
	"larik/internal/permission"
)

// The original approval covers the workspace operation, but it must not
// override explicit denials on destinations discovered by the language server.
func (a *Agent) checkWriteDestination(use llm.Block, path string) error {
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
	if decision == permission.Deny {
		return fmt.Errorf("permission denied: %s", reason)
	}
	return nil
}
