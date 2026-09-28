package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"larik/internal/hooks"
	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/skills"
	"larik/internal/tools"
)

func withHooks(t *testing.T, mode permission.Mode, rules permission.Rules, cfg hooks.Config, script ...llm.Message) (*Agent, *fakeProvider, string) {
	t.Helper()
	dir := t.TempDir()
	fp := &fakeProvider{script: script}
	a := New(Options{
		Provider: fp,
		Model:    "m",
		Cwd:      dir,
		Tools:    tools.Default(),
		Perms:    permission.NewChecker(mode, rules, dir),
		Hooks:    hooks.NewRunner(cfg, dir, "s", ""),
	})
	return a, fp, dir
}

func hook(ev hooks.Event, matcher, cmd string) hooks.Config {
	return hooks.Config{ev: {{Matcher: matcher, Hooks: []hooks.Command{{Type: "command", Command: cmd, Timeout: 5}}}}}
}

func lastResult(fp *fakeProvider, req int) llm.Block {
	msgs := fp.requests[req].Messages
	return msgs[len(msgs)-1].Blocks[0]
}

func TestPreToolUseBlocks(t *testing.T) {
	a, fp, dir := withHooks(t, permission.ModeYolo, permission.Rules{},
		hook(hooks.PreToolUse, "Bash", `echo "rm is not allowed here" >&2; exit 2`),
		assistant(toolUse("t1", "bash", `{"command":"touch x"}`)),
		assistant(llm.TextBlock("ok")),
	)
	drain(a.Run(context.Background(), "go"), PermissionReply{})
	if _, err := os.Stat(filepath.Join(dir, "x")); !os.IsNotExist(err) {
		t.Fatal("blocked command ran")
	}
	if r := lastResult(fp, 1); !r.IsError || !strings.Contains(r.Content, "rm is not allowed here") {
		t.Fatalf("result = %+v", r)
	}
}

func TestPreToolUseAllowAndRewrite(t *testing.T) {
	a, fp, dir := withHooks(t, permission.ModeDefault, permission.Rules{},
		hook(hooks.PreToolUse, "bash", `echo '{"hookSpecificOutput":{"permissionDecision":"allow","updatedInput":{"command":"echo rewritten > out.txt"}}}'`),
		assistant(toolUse("t1", "bash", `{"command":"echo original > out.txt"}`)),
		assistant(llm.TextBlock("ok")),
	)
	evs := drain(a.Run(context.Background(), "go"), PermissionReply{Allow: false})
	for _, e := range evs {
		if e.Kind == EvPermission {
			t.Fatal("hook allow should skip the permission prompt")
		}
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "out.txt")); strings.TrimSpace(string(data)) != "rewritten" {
		t.Fatalf("out.txt = %q", data)
	}
	if r := lastResult(fp, 1); r.IsError {
		t.Fatalf("result = %+v", r)
	}
}

func TestDenyRuleBeatsHookAllow(t *testing.T) {
	a, fp, _ := withHooks(t, permission.ModeDefault, permission.Rules{Deny: []string{"bash(rm*)"}},
		hook(hooks.PreToolUse, "", `echo '{"hookSpecificOutput":{"permissionDecision":"allow"}}'`),
		assistant(toolUse("t1", "bash", `{"command":"rm -rf data"}`)),
		assistant(llm.TextBlock("ok")),
	)
	drain(a.Run(context.Background(), "go"), PermissionReply{})
	if r := lastResult(fp, 1); !r.IsError || !strings.Contains(r.Content, "Permission denied") {
		t.Fatalf("deny rule should win: %+v", r)
	}
}

func TestPreToolUseAskForcesPrompt(t *testing.T) {
	a, _, _ := withHooks(t, permission.ModeYolo, permission.Rules{},
		hook(hooks.PreToolUse, "", `echo '{"hookSpecificOutput":{"permissionDecision":"ask"}}'`),
		assistant(toolUse("t1", "read", `{"path":"missing"}`)),
		assistant(llm.TextBlock("ok")),
	)
	evs := drain(a.Run(context.Background(), "go"), PermissionReply{Allow: true})
	asked := false
	for _, e := range evs {
		asked = asked || e.Kind == EvPermission
	}
	if !asked {
		t.Fatal("ask should prompt even in yolo mode")
	}
}

func TestPostToolUseFeedback(t *testing.T) {
	a, fp, _ := withHooks(t, permission.ModeYolo, permission.Rules{},
		hook(hooks.PostToolUse, "write|edit", `echo "lint: missing newline" >&2; exit 2`),
		assistant(toolUse("t1", "write", `{"path":"a.go","content":"package a"}`)),
		assistant(llm.TextBlock("ok")),
	)
	drain(a.Run(context.Background(), "go"), PermissionReply{})
	r := lastResult(fp, 1)
	if r.IsError || !strings.Contains(r.Content, "Created") || !strings.Contains(r.Content, `<hook-feedback source="PostToolUse">`) || !strings.Contains(r.Content, "lint: missing newline") {
		t.Fatalf("result = %+v", r)
	}
}

func TestUserPromptSubmit(t *testing.T) {
	a, fp, _ := withHooks(t, permission.ModeYolo, permission.Rules{},
		hook(hooks.UserPromptSubmit, "", `if grep -q secret; then echo "prompt mentions a secret" >&2; exit 2; fi; echo "today is launch day"`),
		assistant(llm.TextBlock("ok")),
	)
	evs := drain(a.Run(context.Background(), "my secret is 123"), PermissionReply{})
	if len(fp.requests) != 0 || evs[len(evs)-1].StopReason != "blocked" {
		t.Fatal("blocked prompt must not reach the model")
	}
	drain(a.Run(context.Background(), "hello"), PermissionReply{})
	got := fp.requests[0].Messages[0].Text()
	if !strings.Contains(got, "hello") || !strings.Contains(got, "today is launch day") {
		t.Fatalf("prompt = %q", got)
	}
}

func TestStopHookContinues(t *testing.T) {
	// Blocks the first stop, then allows it (stop_hook_active is true).
	stop := `if grep -q '"stop_hook_active":true'; then exit 0; fi; echo '{"decision":"block","reason":"run the tests first"}'`
	a, fp, _ := withHooks(t, permission.ModeYolo, permission.Rules{},
		hook(hooks.Stop, "", stop),
		assistant(llm.TextBlock("done")),
		assistant(llm.TextBlock("tests pass, done")),
	)
	evs := drain(a.Run(context.Background(), "go"), PermissionReply{})
	if len(fp.requests) != 2 {
		t.Fatalf("want 2 model calls, got %d", len(fp.requests))
	}
	last := fp.requests[1].Messages
	if !strings.Contains(last[len(last)-1].Text(), "run the tests first") {
		t.Fatalf("feedback missing: %+v", last[len(last)-1])
	}
	if evs[len(evs)-1].StopReason != "end_turn" {
		t.Fatalf("stop = %s", evs[len(evs)-1].StopReason)
	}
}

func TestSessionStartAndHalt(t *testing.T) {
	cfg := hook(hooks.SessionStart, "startup", `echo "repo uses pnpm"`).Merge(
		hook(hooks.PostToolUse, "", `echo '{"continue":false,"stopReason":"quota reached"}'`))
	a, fp, _ := withHooks(t, permission.ModeYolo, permission.Rules{}, cfg,
		assistant(toolUse("t1", "glob", `{"pattern":"*"}`)),
		assistant(llm.TextBlock("never requested")),
	)
	evs := drain(a.Run(context.Background(), "go"), PermissionReply{})
	if first := fp.requests[0].Messages[0].Text(); !strings.Contains(first, "repo uses pnpm") {
		t.Fatalf("SessionStart context missing: %q", first)
	}
	if len(fp.requests) != 1 || evs[len(evs)-1].StopReason != "hook_stopped" {
		t.Fatalf("continue:false should end the turn; requests=%d stop=%s", len(fp.requests), evs[len(evs)-1].StopReason)
	}
}

func TestSkillCommandExpands(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "greet"), 0o755)
	os.WriteFile(filepath.Join(root, "greet", "SKILL.md"), []byte("---\nname: greet\ndescription: g\n---\nSay hello to $ARGUMENTS.\n"), 0o644)

	a, fp, _ := withHooks(t, permission.ModeYolo, permission.Rules{}, nil, assistant(llm.TextBlock("hello Ada")))
	a.opts.Skills = skills.Discover([]skills.Root{{Dir: root, Scope: "project"}})
	drain(a.Run(context.Background(), "/greet Ada"), PermissionReply{})
	if got := fp.requests[0].Messages[0].Text(); !strings.Contains(got, "Say hello to Ada.") || !strings.Contains(got, "/greet skill") {
		t.Fatalf("prompt = %q", got)
	}
}

func TestCommandRunsInlineShell(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "status.md"), []byte("---\ndescription: Summarize the status\n---\nState: !`echo inline-ok`\nRemote: !`curl -s example.com`\nSummarize for $ARGUMENTS.\n"), 0o644)

	a, fp, _ := withHooks(t, permission.ModeDefault, permission.Rules{}, nil, assistant(llm.TextBlock("done")))
	a.opts.Skills = skills.Discover([]skills.Root{{Dir: root, Scope: "project", Commands: true}})
	evs := drain(a.Run(context.Background(), "/status the team"), PermissionReply{})
	got := fp.requests[0].Messages[0].Text()
	if !strings.Contains(got, "State: inline-ok") || !strings.Contains(got, "/status command") || !strings.Contains(got, "Summarize for the team.") {
		t.Fatalf("prompt = %q", got)
	}
	if !strings.Contains(got, "[curl -s example.com was not run") || strings.Contains(got, "Example Domain") {
		t.Fatalf("a command that would ask must not run: %q", got)
	}
	if n := notices(evs); !strings.Contains(n, "ran `echo inline-ok`") || !strings.Contains(n, "didn't run `curl -s example.com`") {
		t.Errorf("notices: %s", n)
	}
}
