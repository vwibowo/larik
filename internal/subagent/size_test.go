package subagent

import "testing"

// TestTaskDescriptionBudget caps the task tool's description at its
// fullest: worktrees, a delegation policy and three roles. It goes with
// every request; see app.TestToolDefinitionBudget. If a change needs more
// room, raise the limit in the same change and say why.
func TestTaskDescriptionBudget(t *testing.T) {
	tl := &Tool{
		Set:    Discover(nil),
		Repo:   "/repo",
		Policy: func() string { return "balanced" },
		Roles: func() []Role {
			return []Role{
				{Name: "smart", Spec: "anthropic/opus", Hint: "hard problems"},
				{Name: "worker", Spec: "anthropic/sonnet", Hint: "routine edits"},
				{Name: "explore", Spec: "anthropic/haiku", Hint: "searches"},
			}
		},
	}
	if limit, n := 2300, len(tl.Spec().Description); n > limit {
		t.Errorf("task description is %d bytes, over its %d-byte budget: trim it, or raise the limit on purpose", n, limit)
	}
}
