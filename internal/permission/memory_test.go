package permission

import (
	"encoding/json"
	"testing"
)

func TestUserMemoryMutationsRequireApproval(t *testing.T) {
	for _, mode := range []Mode{ModeDefault, ModeAcceptEdits, ModeAuto, ModePlan, ModeYolo} {
		for _, action := range []string{"save", "delete", "read", "list"} {
			for _, scope := range []string{"", "project", "user"} {
				input, _ := json.Marshal(map[string]string{"action": action, "scope": scope, "name": "note"})
				c := NewChecker(mode, Rules{}, t.TempDir())
				got, _ := c.Decide(Call{Tool: MemoryTool, Input: input})
				want := Allow
				if scope == "user" && (action == "save" || action == "delete") && mode != ModeYolo {
					want = Ask
				}
				if got != want {
					t.Errorf("mode=%s %s scope=%q: got %v want %v", mode, action, scope, got, want)
				}
			}
		}
	}
	in := json.RawMessage(`{"action":"save","scope":"user"}`)
	for _, mode := range []Mode{ModeDefault, ModeAuto, ModePlan, ModeYolo} {
		c := NewChecker(mode, Rules{Allow: []string{"memory"}}, t.TempDir())
		if got, _ := c.Decide(Call{Tool: MemoryTool, Input: in}); got != Allow {
			t.Errorf("explicit allow ignored in %s", mode)
		}
		c = NewChecker(mode, Rules{Allow: []string{"memory"}, Deny: []string{"memory"}}, t.TempDir())
		if got, _ := c.Decide(Call{Tool: MemoryTool, Input: in}); got != Deny {
			t.Errorf("deny ignored in %s", mode)
		}
	}
}
