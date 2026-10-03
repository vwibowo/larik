package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/tools"
)

// namedProvider is a funcProvider under another name, standing in for a
// cheaper provider.
type namedProvider struct {
	*funcProvider
	name string
}

func (n namedProvider) Name() string { return n.name }

func withRoles(t *testing.T, a *agent.Agent, roles []Role, resolve Resolver) *Tool {
	t.Helper()
	tl, ok := a.Tools().Get(ToolName)
	if !ok {
		t.Fatal("no task tool")
	}
	task := tl.(*Tool)
	task.Roles = func() []Role { return roles }
	task.Resolve = resolve
	return task
}

func TestTaskModelRoleRunsOnCheaperModel(t *testing.T) {
	main := &funcProvider{}
	cheap := &funcProvider{respond: func(req llm.Request) llm.Message { return text("done cheaply") }}
	main.respond = func(req llm.Request) llm.Message {
		if res := toolResults(req); len(res) > 0 {
			return text("ok: " + res[0].Content)
		}
		return llm.Message{Blocks: []llm.Block{use("p1", "task", `{"description":"rename","prompt":"Rename foo to bar","subagent_type":"general-purpose","model":"worker"}`)}}
	}
	a, _, _ := newParent(t, main, permission.ModeDefault)
	var asked []string
	task := withRoles(t, a, []Role{
		{Name: "worker", Spec: "groq/llama-4-scout", Hint: "routine edits", Price: "$0.11/$0.34 per M"},
		{Name: "explore"}, // unset: not offered
		{Name: "opus", Legacy: true},
	}, func(spec string) (llm.Provider, string, error) {
		asked = append(asked, spec)
		return namedProvider{cheap, "groq"}, "llama-4-scout", nil
	})

	spec := task.Spec()
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(spec.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(schema.Properties["model"].Enum); got != "[inherit worker]" {
		t.Errorf("model enum = %s", got)
	}
	if !strings.Contains(spec.Description, "- worker: groq/llama-4-scout ($0.11/$0.34 per M) — routine edits") {
		t.Errorf("spec lacks the role line:\n%s", spec.Description)
	}

	drain(a.Run(context.Background(), "rename foo"), true)

	if fmt.Sprint(asked) != "[worker]" {
		t.Errorf("resolver asked for %v", asked)
	}
	if len(cheap.requests()) != 1 {
		t.Fatalf("the cheap provider served %d requests, want 1", len(cheap.requests()))
	}
	for _, r := range main.requests() {
		if isChild(r) {
			t.Errorf("a child request went to the main provider")
		}
	}
	// The subagent's spend is priced for the model it ran on.
	sp := a.SpendByModel()
	models := map[string]bool{}
	for _, s := range sp {
		models[s.Model] = true
	}
	if !models["llama-4-scout"] || !models["m"] {
		t.Errorf("spend by model = %+v", sp)
	}
}

// Forwarded child events name the model the subagent ran on, so a front end
// can show which tier is doing the work.
func TestForwardedEventsCarryTheSubagentModel(t *testing.T) {
	main := &funcProvider{}
	cheap := &funcProvider{respond: func(req llm.Request) llm.Message {
		if res := toolResults(req); len(res) > 0 {
			return text("found it")
		}
		return llm.Message{Blocks: []llm.Block{use("c1", "grep", `{"pattern":"package"}`)}}
	}}
	main.respond = func(req llm.Request) llm.Message {
		if res := toolResults(req); len(res) > 0 {
			return text("ok: " + res[0].Content)
		}
		return llm.Message{Blocks: []llm.Block{use("p1", "task", `{"description":"find it","prompt":"Where is it?","subagent_type":"explore","model":"worker"}`)}}
	}
	a, _, _ := newParent(t, main, permission.ModeDefault)
	withRoles(t, a, []Role{{Name: "worker", Spec: "groq/llama-4-scout"}}, func(string) (llm.Provider, string, error) {
		return namedProvider{cheap, "groq"}, "llama-4-scout", nil
	})

	forwarded := 0
	for _, e := range drain(a.Run(context.Background(), "find it"), true) {
		if e.Agent == "" {
			continue
		}
		forwarded++
		if e.Model != "llama-4-scout" {
			t.Errorf("forwarded %s event carries model %q, want the subagent's", e.Kind, e.Model)
		}
	}
	if forwarded == 0 {
		t.Fatal("no child events were forwarded")
	}
}

func TestTaskModelPrecedence(t *testing.T) {
	parentP := &funcProvider{respond: func(llm.Request) llm.Message { return text("x") }}
	parent := agent.New(agent.Options{Provider: parentP, Model: "main-model", Tools: tools.NewRegistry()})
	var asked []string
	tl := &Tool{
		Roles: func() []Role {
			return []Role{{Name: "worker", Spec: "groq/w"}, {Name: "explore"}, {Name: "haiku", Legacy: true}}
		},
		Resolve: func(spec string) (llm.Provider, string, error) {
			asked = append(asked, spec)
			if spec == "broken/x" {
				return nil, "", fmt.Errorf("no key")
			}
			return parentP, "resolved-" + spec, nil
		},
	}
	cases := []struct {
		defModel, asked string
		wantModel       string
		wantNotice      string
	}{
		{"", "", "main-model", ""},
		{"worker", "", "resolved-worker", ""},
		{"worker", "inherit", "main-model", ""},       // the task overrides the definition
		{"explore", "", "main-model", ""},             // an unset role inherits
		{"", "groq/raw", "resolved-groq/raw", ""},     // a spec works too
		{"haiku", "", "main-model", "a Claude alias"}, // unmapped alias off Anthropic
		{"", "broken/x", "main-model", "unavailable (no key)"},
	}
	for _, c := range cases {
		_, model, notice := tl.model(Definition{Name: "d", Model: c.defModel}, c.asked, parent)
		if model != c.wantModel || !strings.Contains(notice, c.wantNotice) || (c.wantNotice == "" && notice != "") {
			t.Errorf("def %q asked %q: model %q notice %q", c.defModel, c.asked, model, notice)
		}
	}
}

func TestNoRolesMeansNoModelInput(t *testing.T) {
	tl := &Tool{Set: Discover(nil)}
	if strings.Contains(string(tl.Spec().Schema), `"model"`) {
		t.Errorf("model input offered without roles")
	}
	tl.Roles = func() []Role { return []Role{{Name: "worker"}, {Name: "opus", Legacy: true}} }
	if strings.Contains(string(tl.Spec().Schema), `"model"`) {
		t.Errorf("model input offered when every role inherits")
	}
}

// roleParent runs a parent that delegates once to general-purpose (the
// worker role); the child answers with childTurn until it is stopped.
func roleParent(t *testing.T, repo string, role Role, childTurn func(n int) llm.Message) (string, bool, int) {
	t.Helper()
	fp := &funcProvider{}
	var mu sync.Mutex
	childTurns := 0
	fp.respond = func(req llm.Request) llm.Message {
		if isChild(req) {
			mu.Lock()
			childTurns++
			n := childTurns
			mu.Unlock()
			return childTurn(n)
		}
		if res := toolResults(req); len(res) > 0 {
			return text("parent done")
		}
		return llm.Message{Blocks: []llm.Block{use("p1", "task", `{"description":"work","prompt":"do it","subagent_type":"general-purpose"}`)}}
	}
	task := &Tool{Set: Discover(nil), Context: "<env>test</env>", Repo: repo, WorktreeRoot: t.TempDir(),
		Roles: func() []Role { return []Role{role} }}
	cwd := repo
	if cwd == "" {
		cwd = t.TempDir()
	}
	a := agent.New(agent.Options{
		Provider: fp, Model: "m", Cwd: cwd,
		Tools: tools.NewRegistry(append(tools.Builtin(), task)...),
		Perms: permission.NewChecker(permission.ModeAcceptEdits, permission.Rules{}, cwd),
	})
	var out string
	var isErr bool
	for _, e := range drain(a.Run(context.Background(), "go"), true) {
		if e.Kind == agent.EvToolEnd && e.ToolName == "task" {
			out, isErr = e.Output, e.IsError
		}
	}
	return out, isErr, childTurns
}

func TestRoleRunsInWorktree(t *testing.T) {
	repo := gitRepo(t)
	out, isErr, _ := roleParent(t, repo, Role{Name: "worker", Isolation: "worktree"}, func(n int) llm.Message {
		if n == 1 {
			return llm.Message{Blocks: []llm.Block{use("c1", "write", `{"path":"new.txt","content":"hi\n"}`)}}
		}
		return text("wrote it")
	})
	if isErr || !strings.Contains(out, "larik/task-") {
		t.Fatalf("the worker role should run in a worktree: %s", out)
	}
	if _, err := os.Stat(filepath.Join(repo, "new.txt")); err == nil {
		t.Errorf("the worker's edit reached the checkout")
	}
}

func TestStuckSubagentIsStopped(t *testing.T) {
	out, isErr, turns := roleParent(t, "", Role{Name: "worker"}, func(n int) llm.Message {
		return llm.Message{Blocks: []llm.Block{llm.TextBlock("Task completed"), use(fmt.Sprint("c", n), "glob", `{"pattern":"*.none"}`)}}
	})
	if !isErr || !strings.Contains(out, "repeated the same glob call") || !strings.Contains(out, "Its last message:\nTask completed") {
		t.Errorf("result = %q (error %v)", out, isErr)
	}
	if turns != 4 {
		t.Errorf("child turns = %d, want 4", turns)
	}
}

func TestRoleTurnCap(t *testing.T) {
	out, isErr, turns := roleParent(t, "", Role{Name: "worker", MaxTurns: 3}, func(n int) llm.Message {
		return llm.Message{Blocks: []llm.Block{use(fmt.Sprint("c", n), "glob", fmt.Sprintf(`{"pattern":"*.x%d"}`, n))}}
	})
	if !isErr || !strings.Contains(out, "ran out of turns (3)") || turns != 3 {
		t.Errorf("result = %q (error %v), turns %d", out, isErr, turns)
	}
}

func TestRoleWorktreeWithoutRepoRunsInPlace(t *testing.T) {
	out, isErr, _ := roleParent(t, "", Role{Name: "worker", Isolation: "worktree"}, func(int) llm.Message { return text("done in place") })
	if isErr || out != "done in place" {
		t.Errorf("result = %q", out)
	}
}

func TestMinimalContextRole(t *testing.T) {
	fp := &funcProvider{}
	fp.respond = func(req llm.Request) llm.Message {
		if isChild(req) {
			return text("done")
		}
		return llm.Message{Blocks: []llm.Block{use("p1", "task", `{"description":"work","prompt":"do it","subagent_type":"general-purpose","model":"worker"}`)}}
	}
	dir := t.TempDir()
	task := &Tool{
		Set:                Discover(nil),
		ContextFunc:        func() string { return "<full>everything, including skills</full>" },
		MinimalContextFunc: func() string { return "<minimal>just this project</minimal>" },
		Roles:              func() []Role { return []Role{{Name: "worker", Context: "minimal"}} },
	}
	a := agent.New(agent.Options{
		Provider: fp, Model: "m", Cwd: dir,
		Tools: tools.NewRegistry(append(tools.Builtin(), task)...),
		Perms: permission.NewChecker(permission.ModeAcceptEdits, permission.Rules{}, dir),
	})
	drain(a.Run(context.Background(), "go"), true)

	var child llm.Request
	for _, r := range fp.requests() {
		if isChild(r) {
			child = r
			break
		}
	}
	if !strings.Contains(child.System, "<minimal>") || strings.Contains(child.System, "<full>") {
		t.Errorf("a minimal-context role should get the minimal prompt, not the full one:\n%s", child.System)
	}
}

// TestSkillsIndexOnlyForSkillUsers: the skills index goes to children
// that may load skills, not to agents like explore whose tool list
// excludes the skill tool.
func TestSkillsIndexOnlyForSkillUsers(t *testing.T) {
	for typ, want := range map[string]bool{"general-purpose": true, "explore": false} {
		fp := &funcProvider{}
		fp.respond = func(req llm.Request) llm.Message {
			if isChild(req) {
				return text("done")
			}
			return llm.Message{Blocks: []llm.Block{use("p1", "task", `{"description":"work","prompt":"do it","subagent_type":"`+typ+`"}`)}}
		}
		dir := t.TempDir()
		task := &Tool{
			Set:         Discover(nil),
			ContextFunc: func() string { return "<env/>" },
			SkillsIndex: func() string { return "<skills>index</skills>" },
		}
		a := agent.New(agent.Options{
			Provider: fp, Model: "m", Cwd: dir,
			Tools: tools.NewRegistry(append(tools.Builtin(), task)...),
			Perms: permission.NewChecker(permission.ModeAcceptEdits, permission.Rules{}, dir),
		})
		drain(a.Run(context.Background(), "go"), true)
		var child llm.Request
		for _, r := range fp.requests() {
			if isChild(r) {
				child = r
				break
			}
		}
		if child.System == "" {
			t.Fatalf("%s: no child request", typ)
		}
		if got := strings.Contains(child.System, "<skills>"); got != want {
			t.Errorf("%s: skills index present = %v, want %v", typ, got, want)
		}
	}
}
