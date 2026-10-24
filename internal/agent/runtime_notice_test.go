package agent

import (
	"context"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/permission"
)

type noticeRuntime struct{}

func (noticeRuntime) Run(_ context.Context, req llm.AgentRuntimeRequest) (<-chan llm.AgentRuntimeEvent, error) {
	ch := make(chan llm.AgentRuntimeEvent, 3)
	msg := llm.Message{Role: llm.RoleAssistant, Model: req.Model, Blocks: []llm.Block{llm.TextBlock("completed")}}
	ch <- llm.AgentRuntimeEvent{Notice: "CLI inactivity warning"}
	ch <- llm.AgentRuntimeEvent{Assistant: &msg}
	ch <- llm.AgentRuntimeEvent{Done: true}
	close(ch)
	return ch, nil
}

func TestRuntimeNoticeIsForwardedWithoutRewritingConversation(t *testing.T) {
	a, _, _ := setup(t, permission.ModeDefault)
	a.opts.Runtime = noticeRuntime{}
	seen := false
	for _, e := range drain(a.Run(context.Background(), "go"), PermissionReply{}) {
		if e.Kind == EvNotice && e.Text == "CLI inactivity warning" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("runtime notice not forwarded")
	}
	for _, msg := range a.messages {
		if strings.Contains(msg.Text(), "CLI inactivity warning") {
			t.Fatal("runtime status entered model conversation")
		}
	}
}
