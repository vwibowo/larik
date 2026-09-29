package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/permission"
)

type fakeMCP struct{}

func (fakeMCP) IsServer(name string) bool { return name == "notes" }
func (fakeMCP) ReadResource(_ context.Context, server, uri string) ([]llm.Block, error) {
	if uri == "missing" {
		return nil, errors.New("no such resource")
	}
	return []llm.Block{{Type: llm.BlockText, Attachment: server + ":" + uri, Text: "<mcp-resource>remember the milk</mcp-resource>"}}, nil
}
func (fakeMCP) ExpandPrompt(_ context.Context, line string) (string, bool, error) {
	switch {
	case strings.HasPrefix(line, "/mcp__notes__review "):
		return "Review " + strings.TrimPrefix(line, "/mcp__notes__review "), true, nil
	case line == "/mcp__notes__review":
		return "", true, errors.New("/mcp__notes__review needs <file>")
	}
	return "", false, nil
}

func TestMCPResourceMention(t *testing.T) {
	a, fp, _ := setup(t, permission.ModeDefault, assistant(llm.TextBlock("ok")))
	a.opts.MCP = fakeMCP{}
	evs := drain(a.Run(context.Background(), "summarize @notes:note://readme and ask @alice, see @notes:missing"), PermissionReply{})
	msg := fp.requests[0].Messages[0]
	var attached []string
	for _, b := range msg.Blocks {
		if b.Attachment != "" {
			attached = append(attached, b.Attachment)
		}
	}
	if len(attached) != 1 || attached[0] != "notes:note://readme" || !strings.Contains(msg.Blocks[1].Text, "remember the milk") {
		t.Fatalf("blocks: %+v", msg.Blocks)
	}
	if n := notices(evs); !strings.Contains(n, "attached @notes:note://readme") || !strings.Contains(n, "couldn't attach @notes:missing") || strings.Contains(n, "alice") {
		t.Fatalf("notices: %s", n)
	}
}

func TestMCPPromptCommand(t *testing.T) {
	a, fp, _ := setup(t, permission.ModeDefault, assistant(llm.TextBlock("ok")))
	a.opts.MCP = fakeMCP{}
	drain(a.Run(context.Background(), "/mcp__notes__review main.go"), PermissionReply{})
	if got := fp.requests[0].Messages[0].Text(); got != "Review main.go" {
		t.Fatalf("prompt = %q", got)
	}
	evs := drain(a.Run(context.Background(), "/mcp__notes__review"), PermissionReply{})
	if last := evs[len(evs)-1]; last.StopReason != "error" || len(fp.requests) != 1 {
		t.Fatalf("a failing prompt shouldn't reach the model: %+v, %d requests", last, len(fp.requests))
	}
}
