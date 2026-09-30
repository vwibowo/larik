package app

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"

	"larik/internal/agent"
	"larik/internal/config"
	"larik/internal/llm"
	"larik/internal/providers"
)

// replyProvider answers every request with reply and keeps the requests.
type replyProvider struct {
	reply    string
	requests []llm.Request
}

func (*replyProvider) Name() string { return "main" }
func (p *replyProvider) Stream(_ context.Context, req llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		p.requests = append(p.requests, req)
		yield(llm.StreamEvent{Type: llm.EventDone, Message: llm.Message{Role: llm.RoleAssistant, Model: req.Model, Blocks: []llm.Block{llm.TextBlock(p.reply)}},
			Usage: llm.Usage{Input: 300, Output: 20}}, nil)
	}
}

func TestParseAutoVerdict(t *testing.T) {
	for reply, want := range map[string]agent.AutoVerdict{
		`{"decision": "allow", "reason": "runs the tests"}`:                   {Allow: true, Reason: "runs the tests"},
		"```json\n{\"decision\":\"ALLOW\",\"reason\":\"fine\"}\n```":          {Allow: true, Reason: "fine"},
		`Here is my verdict: {"decision": "ask", "reason": "pushes"} Thanks.`: {Reason: "pushes"},
		`{"decision": "ask"}`: {Reason: "the automatic check wasn't sure this is safe"},
	} {
		got, err := parseAutoVerdict(reply)
		if err != nil || got != want {
			t.Errorf("%q: got %+v %v, want %+v", reply, got, err, want)
		}
	}
	// Anything that isn't a clear verdict is an error, which asks the user.
	for _, reply := range []string{"", "allow", `{"decision": "yes"}`, `{"decision": allow}`, `{"ok": true}`} {
		if v, err := parseAutoVerdict(reply); err == nil {
			t.Errorf("%q should not parse as a verdict: %+v", reply, v)
		}
	}
}

func TestAutoPrompt(t *testing.T) {
	p := autoPrompt(agent.AutoCall{
		Tool: "bash", Input: json.RawMessage(`{"command":"git push","sandbox":false}`), Cwd: "/w/proj",
		Prompts: []string{"fix the bug", "now ship it"}, Task: "push the branch", Sandboxed: true,
	})
	for _, want := range []string{
		"Working directory: /w/proj",
		"runs outside the OS sandbox",
		"<developer-prompt n=\"1\">\nfix the bug\n</developer-prompt>",
		"<developer-prompt n=\"2\">\nnow ship it\n</developer-prompt>",
		"The main agent (not the developer) gave it this task:\n<subagent-task>\npush the branch",
		"<tool-call tool=\"bash\">\n{\"command\":\"git push\",\"sandbox\":false}\n</tool-call>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "cut short") {
		t.Error("a short input isn't cut")
	}

	long := autoPrompt(agent.AutoCall{Tool: "write", Input: json.RawMessage(`{"content":"` + strings.Repeat("x", autoInputLimit) + `"}`)})
	if !strings.Contains(long, "cut short") || len(long) > autoInputLimit+2000 {
		t.Errorf("a long input should be cut and marked (%d bytes)", len(long))
	}
	if !strings.Contains(long, "(none)") {
		t.Error("no prompts should be said plainly")
	}
	if none := autoPrompt(agent.AutoCall{Tool: "bash", Input: json.RawMessage(`{}`)}); !strings.Contains(none, "No OS sandbox is active") {
		t.Errorf("without a sandbox: %s", none)
	}
}

func TestAutoApproverAsksTheRightModel(t *testing.T) {
	prov := &replyProvider{reply: `{"decision": "ask", "reason": "deletes the repository"}`}
	var asked []string
	a := &App{
		Cfg: &config.Config{Providers: map[string]config.ProviderConfig{}},
		Resolve: func(_ *config.Config, spec string) (providers.Resolved, error) {
			asked = append(asked, spec)
			return providers.Resolved{Provider: prov, Model: "judge"}, nil
		},
	}
	ag := agent.New(agent.Options{Provider: prov, Model: "big"})
	approve := a.autoApprover(ag)
	call := agent.AutoCall{Tool: "bash", Input: json.RawMessage(`{"command":"rm -rf .git"}`), Prompts: []string{"tidy up"}}

	v, err := approve(context.Background(), call)
	if err != nil || v.Allow || v.Reason != "deletes the repository" {
		t.Fatalf("verdict: %+v %v", v, err)
	}
	a.Cfg.AutoMode.Model = "anthropic/claude-haiku-4-5"
	prov.reply = `{"decision": "allow", "reason": "ok"}`
	if v, err := approve(context.Background(), call); err != nil || !v.Allow {
		t.Fatalf("verdict: %+v %v", v, err)
	}
	if len(asked) != 2 || asked[0] != "main/big" || asked[1] != "anthropic/claude-haiku-4-5" {
		t.Errorf("the session's model by default, auto_mode.model when set: %v", asked)
	}
	req := prov.requests[0]
	if req.System != autoSystem || req.Effort != llm.EffortLow || len(req.Tools) != 0 || !strings.Contains(req.Messages[0].Text(), "rm -rf .git") {
		t.Errorf("the check's request: %+v", req)
	}
	if spend := ag.SpendByModel(); len(spend) != 1 || spend[0].Usage.Input != 600 {
		t.Errorf("the check's spend counts toward the session: %+v", spend)
	}

	prov.reply = "I cannot decide"
	if _, err := approve(context.Background(), call); err == nil {
		t.Error("an unreadable verdict is an error, so the user is asked")
	}
}
