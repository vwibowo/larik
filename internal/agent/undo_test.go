package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/tools"
)

// writer is a subagent provider that writes path once, then finishes.
func writer(path, content string) *fakeProvider {
	return &fakeProvider{script: []llm.Message{
		assistant(toolUse("w", "write", `{"path":"`+path+`","content":"`+content+`"}`)),
		assistant(llm.TextBlock("done")),
	}}
}

func present(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

func TestSubagentChangesJoinTheTurnThatRanThem(t *testing.T) {
	parent, _, dir := setup(t, permission.ModeDefault)
	parent.opts.Checkpoints.BeginTurn() // the parent's turn calls the task tool
	child := parent.Spawn(SpawnOptions{Type: "worker", Provider: writer("x.txt", "x"), Model: "m", Tools: tools.Default()})
	drain(child.Run(context.Background(), "write x"), PermissionReply{Allow: true})
	if !present(t, filepath.Join(dir, "x.txt")) {
		t.Fatal("the child did not write")
	}
	if child.checkpointUnit() != nil {
		t.Error("the child's unit should end with its run")
	}
	if paths, err := parent.Undo(); err != nil || len(paths) != 1 {
		t.Fatalf("undo: %v %v", paths, err)
	}
	if present(t, filepath.Join(dir, "x.txt")) {
		t.Error("undoing the turn should revert what its subagent changed")
	}
}

func TestBackgroundSubagentChangesStayOutOfTheNextTurn(t *testing.T) {
	parent, _, dir := setup(t, permission.ModeDefault,
		assistant(toolUse("p", "write", `{"path":"y.txt","content":"y"}`)),
		assistant(llm.TextBlock("done")),
	)
	parent.opts.Checkpoints.BeginTurn() // turn 1 starts a background task
	child := parent.Spawn(SpawnOptions{Type: "worker", Provider: writer("x.txt", "x"), Model: "m", Tools: tools.Default()})
	// The user's next prompt runs; then the task writes, still running.
	drain(parent.Run(context.Background(), "write y"), PermissionReply{Allow: true})
	drain(child.Run(context.Background(), "write x"), PermissionReply{Allow: true})

	paths, err := parent.Undo()
	if err != nil || len(paths) != 1 || filepath.Base(paths[0]) != "y.txt" {
		t.Fatalf("undo the last turn: %v %v", paths, err)
	}
	if !present(t, filepath.Join(dir, "x.txt")) {
		t.Fatal("undoing a turn reverted a background task's change")
	}
	if paths, err := parent.Undo(); err != nil || len(paths) != 1 || present(t, filepath.Join(dir, "x.txt")) {
		t.Fatalf("the task's change is undone on its own: %v %v", paths, err)
	}
}

func TestUndoRefusesWhileABackgroundSubagentChangedTheFile(t *testing.T) {
	parent, _, dir := setup(t, permission.ModeDefault,
		assistant(toolUse("p", "write", `{"path":"p.txt","content":"turn"}`)),
		assistant(llm.TextBlock("done")),
	)
	parent.opts.Checkpoints.BeginTurn()
	child := parent.Spawn(SpawnOptions{Type: "worker", Label: "worker: bg", Provider: &fakeProvider{}, Model: "m", Tools: tools.Default()})
	drain(parent.Run(context.Background(), "write p"), PermissionReply{Allow: true})
	// The task, still running, edits the same file.
	p := filepath.Join(dir, "p.txt")
	if err := child.env.BeforeWrite(p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("task"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := parent.Undo()
	if err == nil || !strings.Contains(err.Error(), `task "worker: bg" is still running`) {
		t.Fatalf("undo = %v, want a refusal naming the running task", err)
	}
	child.EndCheckpoint()
	if _, err := parent.Undo(); err == nil || !strings.Contains(err.Error(), "also changed p.txt") {
		t.Fatalf("undo = %v, want a refusal: the task's change would be lost", err)
	}
	if data, _ := os.ReadFile(p); string(data) != "task" {
		t.Fatalf("a refused undo changed the file: %q", data)
	}
}

func TestGrandchildRunDoesNotEndItsParentsUnit(t *testing.T) {
	parent, _, _ := setup(t, permission.ModeDefault)
	parent.opts.Checkpoints.BeginTurn()
	child := parent.Spawn(SpawnOptions{Type: "worker", Provider: &fakeProvider{}, Model: "m", Tools: tools.Default()})
	grandchild := child.Spawn(SpawnOptions{Type: "worker", Provider: &fakeProvider{script: []llm.Message{assistant(llm.TextBlock("hi"))}}, Model: "m", Tools: tools.Default()})
	drain(grandchild.Run(context.Background(), "hi"), PermissionReply{Allow: true})
	if grandchild.checkpointUnit() != nil {
		t.Error("the grandchild's unit should end with its run")
	}
	if child.checkpointUnit() == nil {
		t.Fatal("the grandchild's run ended its parent's unit")
	}
	child.EndCheckpoint()
	child.EndCheckpoint()
	if child.checkpointUnit() != nil {
		t.Error("EndCheckpoint should end the unit")
	}
}

func TestWorktreeSubagentHasNoUnit(t *testing.T) {
	parent, _, _ := setup(t, permission.ModeDefault)
	child := parent.Spawn(SpawnOptions{Type: "worker", Provider: &fakeProvider{}, Model: "m", Tools: tools.Default(), Cwd: t.TempDir()})
	if child.checkpointUnit() != nil || child.env.BeforeWrite != nil {
		t.Error("a worktree subagent's changes are not checkpointed")
	}
}
