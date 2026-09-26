package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runner(t *testing.T, ev Event, matcher string, cmds ...string) (*Runner, string) {
	t.Helper()
	dir := t.TempDir()
	m := Matcher{Matcher: matcher}
	for _, c := range cmds {
		m.Hooks = append(m.Hooks, Command{Type: "command", Command: c, Timeout: 2})
	}
	return NewRunner(Config{ev: {m}}, dir, "sess-1", "/tmp/t.jsonl"), dir
}

func TestPayloadAndEnv(t *testing.T) {
	r, dir := runner(t, PreToolUse, "", `cat > payload.json; echo "$LARIK_PROJECT_DIR" > env.txt`)
	r.Run(context.Background(), Input{HookEventName: PreToolUse, ToolName: "bash", ToolInput: json.RawMessage(`{"command":"ls"}`)}, "bash")

	var got map[string]any
	data, _ := os.ReadFile(filepath.Join(dir, "payload.json"))
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("payload %q: %v", data, err)
	}
	if got["session_id"] != "sess-1" || got["hook_event_name"] != "PreToolUse" || got["tool_name"] != "bash" || got["cwd"] != dir {
		t.Errorf("payload = %v", got)
	}
	if env, _ := os.ReadFile(filepath.Join(dir, "env.txt")); strings.TrimSpace(string(env)) != dir {
		t.Errorf("LARIK_PROJECT_DIR = %q", env)
	}
}

func TestExitCodes(t *testing.T) {
	r, _ := runner(t, PreToolUse, "", `echo "no rm please" >&2; exit 2`)
	res := r.Run(context.Background(), Input{HookEventName: PreToolUse}, "bash")
	if !res.Block || res.Permission != "deny" || res.Reason != "no rm please" {
		t.Errorf("exit 2: %+v", res)
	}

	r, _ = runner(t, PostToolUse, "", `echo oops >&2; exit 1`)
	res = r.Run(context.Background(), Input{HookEventName: PostToolUse}, "bash")
	if res.Block || len(res.Messages) != 1 || !strings.Contains(res.Messages[0], "oops") {
		t.Errorf("exit 1 should be a non-blocking message: %+v", res)
	}

	r, _ = runner(t, UserPromptSubmit, "", `echo "branch: main"`)
	res = r.Run(context.Background(), Input{HookEventName: UserPromptSubmit}, "")
	if len(res.Context) != 1 || res.Context[0] != "branch: main" {
		t.Errorf("plain stdout on UserPromptSubmit should become context: %+v", res)
	}
}

func TestJSONOutput(t *testing.T) {
	r, _ := runner(t, PreToolUse, "", `echo '{"hookSpecificOutput":{"permissionDecision":"allow","updatedInput":{"command":"ls -la"}},"systemMessage":"rewrote ls"}'`)
	res := r.Run(context.Background(), Input{HookEventName: PreToolUse}, "bash")
	if res.Permission != "allow" || string(res.UpdatedInput) != `{"command":"ls -la"}` || res.Messages[0] != "rewrote ls" {
		t.Errorf("%+v", res)
	}

	r, _ = runner(t, Stop, "", `echo '{"decision":"block","reason":"tests are failing"}'`, `echo '{"continue":false,"stopReason":"budget"}'`)
	res = r.Run(context.Background(), Input{HookEventName: Stop}, "")
	if !res.Block || res.Reason != "tests are failing" || !res.Halt || res.HaltReason != "budget" {
		t.Errorf("%+v", res)
	}
}

func TestCombinePrefersDeny(t *testing.T) {
	r, _ := runner(t, PreToolUse, "",
		`echo '{"hookSpecificOutput":{"permissionDecision":"allow"}}'`,
		`echo '{"hookSpecificOutput":{"permissionDecision":"ask"}}'`,
		`echo '{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"nope"}}'`)
	res := r.Run(context.Background(), Input{HookEventName: PreToolUse}, "bash")
	if res.Permission != "deny" || !res.Block || !strings.Contains(res.Reason, "nope") {
		t.Errorf("%+v", res)
	}
}

func TestMatcher(t *testing.T) {
	for _, c := range []struct {
		pattern, target string
		want            bool
	}{
		{"", "bash", true},
		{"*", "edit", true},
		{"Bash", "bash", true}, // Claude Code capitalization works
		{"edit|write", "write", true},
		{"edit|write", "bash", false},
		{"mcp__github__.*", "mcp__github__create_issue", true},
		{"read", "readme", false}, // anchored
	} {
		if got := matches(c.pattern, c.target); got != c.want {
			t.Errorf("matches(%q,%q)=%v", c.pattern, c.target, got)
		}
	}
}

func TestTimeout(t *testing.T) {
	r, _ := runner(t, Stop, "", `sleep 30`)
	start := time.Now()
	res := r.Run(context.Background(), Input{HookEventName: Stop}, "")
	if time.Since(start) > 5*time.Second || len(res.Messages) != 1 || !strings.Contains(res.Messages[0], "timed out") {
		t.Errorf("timeout not enforced: %+v after %s", res, time.Since(start))
	}
}

func TestHash(t *testing.T) {
	a := Config{Stop: {{Hooks: []Command{{Type: "command", Command: "x"}}}}}
	b := Config{Stop: {{Hooks: []Command{{Type: "command", Command: "y"}}}}}
	if a.Hash() == b.Hash() || a.Hash() != (Config{Stop: a[Stop]}).Hash() || (Config{}).Hash() != "" {
		t.Error("hash should identify content")
	}
}

func TestNilRunner(t *testing.T) {
	var r *Runner
	if r.Has(Stop) || r.Run(context.Background(), Input{HookEventName: Stop}, "").Block {
		t.Error("nil runner must be inert")
	}
}
