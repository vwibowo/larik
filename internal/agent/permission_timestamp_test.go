package agent

import (
	"context"
	"testing"
	"time"

	"larik/internal/llm"
	"larik/internal/permission"
)

func TestPermissionRequestStampedAtCreation(t *testing.T) {
	a, _, _ := setup(t, permission.ModeDefault, assistant(toolUse("write", "write", `{"path":"new.txt","content":"new"}`)), assistant(llm.TextBlock("done")))
	start := time.Now()
	seen := false
	for e := range a.Run(context.Background(), "write the file") {
		if e.Kind == EvPermission {
			seen = true
			if e.RequestedAt.IsZero() || e.RequestedAt.Before(start) || e.RequestedAt.After(time.Now()) {
				t.Errorf("invalid request timestamp: %v", e.RequestedAt)
			}
			e.Reply <- PermissionReply{Allow: false}
		}
	}
	if !seen {
		t.Fatal("test did not ask for permission")
	}
}
