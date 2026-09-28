package headless

import (
	"bytes"
	"context"
	"errors"
	"iter"
	"testing"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/tools"
)

type canceledProvider struct{}

func (canceledProvider) Name() string { return "test" }
func (canceledProvider) Stream(ctx context.Context, _ llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) { yield(llm.StreamEvent{}, ctx.Err()) }
}

func TestCanceledRunReturnsError(t *testing.T) {
	dir := t.TempDir()
	a := agent.New(agent.Options{
		Provider: canceledProvider{}, Model: "test", Cwd: dir,
		Tools: tools.NewRegistry(), Perms: permission.NewChecker(permission.ModeDefault, permission.Rules{}, dir),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errOut bytes.Buffer
	if err := Run(ctx, a, "hello", FormatText, &out, &errOut); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled run returned %v", err)
	}
}

func TestTodoListPrintsAsChecklist(t *testing.T) {
	var out, errOut bytes.Buffer
	p := &printer{format: FormatText, stdout: &out, stderr: &errOut, atLineStart: true}
	p.handle(agent.Event{Kind: agent.EvToolStart, ToolName: tools.TodoToolName, Input: []byte(`{"todos":[{"content":"Fix the bug","status":"in_progress"},{"content":"Run the tests","status":"pending"}]}`)})
	if got := errOut.String(); got != "→ tasks\n  [>] Fix the bug\n  [ ] Run the tests\n" {
		t.Fatalf("stderr %q", got)
	}
}
