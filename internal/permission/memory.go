package permission

import "encoding/json"

// UserMemoryMutation changes notes used by every project. The auto-mode
// classifier must not grant this cross-project permission on the user's behalf.
// Project saves and deletes default to project scope in the memory tool.
func UserMemoryMutation(tool string, input json.RawMessage) bool {
	if tool != MemoryTool {
		return false
	}
	var in struct {
		Action string `json:"action"`
		Scope  string `json:"scope"`
	}
	if json.Unmarshal(input, &in) != nil {
		return false
	}
	return in.Scope == "user" && (in.Action == "save" || in.Action == "delete")
}
