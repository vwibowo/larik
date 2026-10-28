package agent

import (
	"context"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/permission"
)

// A background command started by the main agent becomes one of its
// background tasks, marked as a command, and its result is delivered
// like a subagent's.
func TestBashBackgroundIsATask(t *testing.T) {
	a, _, _ := setup(t, permission.ModeYolo,
		assistant(toolUse("b1", "bash", `{"command":"echo from-the-background","run_in_background":true}`)),
		assistant(llm.TextBlock("started")),
	)
	drain(a.Run(context.Background(), "start it"), PermissionReply{Allow: true})
	tasks, err := a.WaitBackground(context.Background(), nil)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks %+v, err %v", tasks, err)
	}
	if got := tasks[0]; !got.Command || got.Status != BgCompleted || !strings.Contains(got.Result, "from-the-background") {
		t.Fatalf("task %+v", got)
	}
}
