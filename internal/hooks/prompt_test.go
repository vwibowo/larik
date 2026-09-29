package hooks

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func promptRunner(ev Event, prompt string, reply func(model, system, prompt string) (string, error)) *Runner {
	r := NewRunner(Config{ev: {{Hooks: []Command{{Type: "prompt", Prompt: prompt, Model: "worker"}}}}}, "/w", "sess-1", "/t.jsonl")
	r.SetEvaluator(func(_ context.Context, model, system, prompt string) (string, error) {
		return reply(model, system, prompt)
	})
	return r
}

func TestPromptHookVerdicts(t *testing.T) {
	var seenModel, seenPrompt string
	r := promptRunner(Stop, "Are all tests passing? Input: $ARGUMENTS", func(model, system, prompt string) (string, error) {
		seenModel, seenPrompt = model, prompt
		if !strings.Contains(system, `{"ok": false`) {
			t.Error("the system prompt should ask for the JSON verdict")
		}
		return "```json\n{\"ok\": false, \"reason\": \"run the tests first\"}\n```", nil
	})
	res := r.Run(context.Background(), Input{HookEventName: Stop}, "")
	if !res.Block || res.Reason != "run the tests first" || res.Permission != "" {
		t.Fatalf("stop: %+v", res)
	}
	if seenModel != "worker" || !strings.Contains(seenPrompt, `Input: {"session_id":"sess-1"`) || strings.Contains(seenPrompt, "$ARGUMENTS") {
		t.Fatalf("model %q prompt %q", seenModel, seenPrompt)
	}

	deny := promptRunner(PreToolUse, "Allow only reads.", func(_, _, prompt string) (string, error) {
		if !strings.Contains(prompt, "Hook input:\n") || !strings.Contains(prompt, `"tool_name":"bash"`) {
			t.Errorf("without $ARGUMENTS the input is appended: %q", prompt)
		}
		return `{"ok": false, "reason": "no writes"}`, nil
	})
	if res := deny.Run(context.Background(), Input{HookEventName: PreToolUse, ToolName: "bash"}, "bash"); !res.Block || res.Permission != "deny" || res.Reason != "no writes" {
		t.Fatalf("pre-tool deny: %+v", res)
	}

	cases := map[string]struct {
		reply string
		err   error
		want  string // a message, or "" for a clean pass
	}{
		"ok":      {reply: `{"ok": true}`},
		"legacy":  {reply: `{"decision": "approve"}`},
		"prose":   {reply: "Looks fine to me.", want: "didn't answer with JSON"},
		"no ok":   {reply: `{"verdict": "yes"}`, want: `no "ok"`},
		"failure": {err: errors.New("rate limited"), want: "failed: rate limited"},
	}
	for name, c := range cases {
		r := promptRunner(Stop, "check", func(string, string, string) (string, error) { return c.reply, c.err })
		res := r.Run(context.Background(), Input{HookEventName: Stop}, "")
		if res.Block || (c.want == "") != (len(res.Messages) == 0) || (c.want != "" && !strings.Contains(res.Messages[0], c.want)) {
			t.Errorf("%s: %+v", name, res)
		}
	}

	none := NewRunner(Config{Stop: {{Hooks: []Command{{Type: "prompt", Prompt: "check"}}}}}, "/w", "s", "/t")
	if res := none.Run(context.Background(), Input{HookEventName: Stop}, ""); res.Block || len(res.Messages) != 1 {
		t.Fatalf("without a model the hook is skipped with a message: %+v", res)
	}
}
