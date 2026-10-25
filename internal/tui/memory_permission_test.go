package tui

import (
	"strings"
	"testing"
)

func TestUserMemoryPermissionExplainsCrossProjectScope(t *testing.T) {
	m := testModel(t)
	for _, action := range []string{"save", "delete"} {
		m.perm = permEvent("memory", map[string]any{"action": action, "scope": "user", "name": "shared", "content": "a note"})
		view := plain(m.permissionView())
		if !strings.Contains(view, "user-scope memory?") || !strings.Contains(view, "every project") || !strings.Contains(view, "shared") {
			t.Fatalf("scope warning missing: %s", view)
		}
	}
}
