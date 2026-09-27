package bench

import (
	"context"
	"fmt"
	"os"
	"time"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/tools"
)

// Result is one task run against one model.
type Result struct {
	Task      string
	Model     string
	Pass      bool
	Detail    string // why it failed, or Setup/agent error
	CostUSD   float64
	Duration  time.Duration
	ToolCalls int
}

// Run sets up task in a fresh temporary directory, gives its prompt to a
// new agent running provider/model with every tool allowed (the fixture
// directory is thrown away afterwards, so there is nothing to protect),
// then verifies the outcome. It never returns an error: a setup or agent
// failure comes back as a failing Result with Detail explaining why, so a
// whole run of several models and tasks doesn't stop on one bad model.
func Run(ctx context.Context, task Task, provider llm.Provider, model string, timeout time.Duration) Result {
	res := Result{Task: task.Name, Model: model}
	dir, err := os.MkdirTemp("", "larik-bench-")
	if err != nil {
		res.Detail = "creating the fixture directory: " + err.Error()
		return res
	}
	defer os.RemoveAll(dir)

	if err := task.Setup(dir); err != nil {
		res.Detail = "setting up the fixture: " + err.Error()
		return res
	}

	a := agent.New(agent.Options{
		Provider: provider,
		Model:    model,
		Cwd:      dir,
		MaxTurns: 40,
		Tools:    tools.Default(),
		Perms:    permission.NewChecker(permission.ModeYolo, permission.Rules{}, dir),
	})

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	for e := range a.Run(runCtx, task.Prompt) {
		switch e.Kind {
		case agent.EvToolEnd:
			res.ToolCalls++
		case agent.EvPermission:
			e.Reply <- agent.PermissionReply{Allow: true}
		}
	}
	res.Duration = time.Since(start)
	res.CostUSD = a.Stats().CostUSD

	if runCtx.Err() != nil {
		res.Detail = fmt.Sprintf("timed out after %s", timeout)
		return res
	}
	res.Pass, res.Detail = task.Verify(dir)
	return res
}
