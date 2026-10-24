package app

import (
	"context"
	"encoding/json"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"larik/internal/config"
	"larik/internal/llm"
	"larik/internal/providers"
	"larik/internal/tools"
)

type credentialProbe struct {
	t      *testing.T
	secret string
	ran    *bool
}

func (credentialProbe) ReadOnly() bool { return true }
func (credentialProbe) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: "credential_probe", Schema: json.RawMessage(`{"type":"object","properties":{}}`)}
}
func (p credentialProbe) Run(ctx context.Context, env *tools.Env, _ json.RawMessage) tools.Result {
	*p.ran = true
	input, _ := json.Marshal(map[string]string{"path": p.secret})
	if result := (tools.Read{}).Run(ctx, env, input); !result.IsError || strings.Contains(result.Content, "PRIVATE_TOKEN") {
		p.t.Errorf("credential read allowed: %+v", result)
	}
	if env.SanitizeEnv == nil {
		p.t.Error("environment scrubber missing")
		return tools.Result{}
	}
	got := strings.Join(env.SanitizeEnv([]string{"ANTHROPIC_API_KEY=hidden", "UNKNOWN_API_KEY=hidden", "KEPT_API_KEY=allowed", "PATH=ok"}), ";")
	if strings.Contains(got, "hidden") || !strings.Contains(got, "KEPT_API_KEY=allowed") || !strings.Contains(got, "PATH=ok") {
		p.t.Errorf("scrubbed environment = %q", got)
	}
	return tools.Result{Content: "checked"}
}

type credentialProvider struct{ calls int }

func (*credentialProvider) Name() string { return "local" }
func (p *credentialProvider) Stream(_ context.Context, req llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		msg := llm.Message{Role: llm.RoleAssistant, Model: req.Model, Blocks: []llm.Block{llm.TextBlock("done")}}
		stop := llm.StopEnd
		if p.calls == 0 {
			msg.Blocks = []llm.Block{{Type: llm.BlockToolUse, ID: "probe", Name: "credential_probe", Input: json.RawMessage(`{}`)}}
			stop = llm.StopToolUse
		}
		p.calls++
		yield(llm.StreamEvent{Type: llm.EventDone, Message: msg, StopReason: stop}, nil)
	}
}

func TestAppActivatesCredentialPolicyWithoutSandbox(t *testing.T) {
	cwd := telemetryEnv(t, `{`+localProvider+`,"execution":"tools","sandbox":{"enabled":false,"env_passthrough":["KEPT_API_KEY"]}}`)
	a, err := Setup(cwd, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	secret := filepath.Join(a.Cfg.ConfigDir, "chatgpt-auth.json")
	if err := os.WriteFile(secret, []byte("PRIVATE_TOKEN"), 0600); err != nil {
		t.Fatal(err)
	}
	ran := false
	a.tools = append(a.tools, credentialProbe{t: t, secret: secret, ran: &ran})
	a.MCP.Base = a.tools
	a.Resolve = func(*config.Config, string) (providers.Resolved, error) {
		return providers.Resolved{Provider: &credentialProvider{}, Model: "test"}, nil
	}
	s, err := a.Open(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close("other")
	for range s.Agent.Run(context.Background(), "check policy") {
	}
	if !ran {
		t.Fatal("probe did not run")
	}
}
