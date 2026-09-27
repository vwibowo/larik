package subagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/tools"
	"larik/internal/worktree"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	for _, args := range [][]string{{"init", "-q"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	return repo
}

// worktreeParent runs a parent whose child, in a worktree, performs
// childCall once and then reports.
func worktreeParent(t *testing.T, repo, childCall string, taskInput string) (*funcProvider, []agent.Event, string) {
	t.Helper()
	fp := &funcProvider{}
	fp.respond = func(req llm.Request) llm.Message {
		if isChild(req) {
			if len(toolResults(req)) > 0 {
				return text("child done")
			}
			if childCall == "" {
				return text("nothing to change")
			}
			return llm.Message{Blocks: []llm.Block{use("c1", "write", childCall)}}
		}
		if res := toolResults(req); len(res) > 0 {
			return text("parent saw: " + res[0].Content)
		}
		return llm.Message{Blocks: []llm.Block{use("p1", "task", taskInput)}}
	}
	task := &Tool{Set: Discover(nil), Context: "<env>test</env>", Repo: repo, WorktreeRoot: t.TempDir()}
	a := agent.New(agent.Options{
		Provider: fp,
		Model:    "m",
		Cwd:      repo,
		Tools:    tools.NewRegistry(append(tools.Builtin(), task)...),
		Perms:    permission.NewChecker(permission.ModeAcceptEdits, permission.Rules{}, repo),
	})
	evs := drain(a.Run(context.Background(), "go"), false)
	var result string
	for _, e := range evs {
		if e.Kind == agent.EvToolEnd && e.ToolName == "task" {
			result = e.Output
		}
	}
	return fp, evs, result
}

func TestWorktreeIsolation(t *testing.T) {
	repo := gitRepo(t)
	fp, evs, result := worktreeParent(t, repo, `{"path":"feature.txt","content":"built in isolation\n"}`,
		`{"description":"add feature","prompt":"add a feature","subagent_type":"general-purpose","isolation":"worktree"}`)

	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); err == nil {
		t.Error("the child wrote into the parent's checkout")
	}
	for _, e := range evs {
		// accept-edits: writes inside the child's own directory need no prompt.
		if e.Kind == agent.EvPermission {
			t.Errorf("unexpected permission prompt: %+v", e)
		}
	}
	list, _ := worktree.List(context.Background(), repo)
	if len(list) != 1 || list[0].Commits != 1 {
		t.Fatalf("worktrees: %+v\nresult: %s", list, result)
	}
	out, _ := exec.Command("git", "-C", repo, "show", list[0].Branch+":feature.txt").Output()
	if string(out) != "built in isolation\n" {
		t.Errorf("branch content: %q", out)
	}
	if !strings.Contains(result, "child done") || !strings.Contains(result, "git merge "+list[0].Branch) {
		t.Errorf("result:\n%s", result)
	}
	var sawNote bool
	for _, r := range fp.requests() {
		sawNote = sawNote || (isChild(r) && strings.Contains(r.System, "<worktree>") && strings.Contains(r.System, filepath.Base(list[0].Path)))
	}
	if !sawNote {
		t.Error("child prompt lacks the worktree note")
	}
}

func TestWorktreeRemovedWhenUnchanged(t *testing.T) {
	repo := gitRepo(t)
	_, _, result := worktreeParent(t, repo, "",
		`{"description":"look","prompt":"look around","subagent_type":"explore","isolation":"worktree"}`)
	if !strings.Contains(result, "no changes") {
		t.Errorf("result: %s", result)
	}
	if list, _ := worktree.List(context.Background(), repo); len(list) != 0 {
		t.Errorf("worktree kept: %+v", list)
	}
}

func TestWorktreeWritesOutsideAsk(t *testing.T) {
	repo := gitRepo(t)
	// The child tries to write into the parent's checkout by absolute path:
	// outside its own directory, so accept-edits asks (and drain denies).
	_, evs, _ := worktreeParent(t, repo, `{"path":"`+filepath.Join(repo, "escape.txt")+`","content":"x"}`,
		`{"description":"x","prompt":"x","subagent_type":"general-purpose","isolation":"worktree"}`)
	var asked bool
	for _, e := range evs {
		asked = asked || (e.Kind == agent.EvPermission && e.ToolName == "write")
	}
	if !asked {
		t.Error("write outside the worktree should ask")
	}
	if _, err := os.Stat(filepath.Join(repo, "escape.txt")); err == nil {
		t.Error("denied write happened")
	}
}

func TestIsolationNeedsRepo(t *testing.T) {
	tl := &Tool{Set: Discover(nil)}
	if strings.Contains(string(tl.Spec().Schema), "isolation") {
		t.Error("isolation offered outside a git repository")
	}
	res := tl.Run(context.Background(), nil, []byte(`{"description":"x","prompt":"x","subagent_type":"explore","isolation":"worktree"}`))
	if !res.IsError {
		t.Errorf("want an error, got %q", res.Content)
	}
}
