package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/tools"
)

// replyProvider answers each request with the next scripted text and
// records the prompts it was sent.
type replyProvider struct {
	replies []string
	prompts []string
}

func (*replyProvider) Name() string { return "test" }
func (p *replyProvider) Stream(_ context.Context, req llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		p.prompts = append(p.prompts, req.Messages[len(req.Messages)-1].Text())
		text := p.replies[0]
		p.replies = p.replies[1:]
		msg := llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{llm.TextBlock(text)}, Model: "test"}
		if !yield(llm.StreamEvent{Type: llm.EventTextDelta, Text: text}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.EventDone, Message: msg, StopReason: llm.StopEnd}, nil)
	}
}

const answerSchema = `{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"]}`

func runStructured(t *testing.T, format Format, replies ...string) (string, *replyProvider, error) {
	t.Helper()
	dir := t.TempDir()
	p := &replyProvider{replies: replies}
	a := agent.New(agent.Options{
		Provider: p, Model: "test", Cwd: dir,
		Tools: tools.NewRegistry(), Perms: permission.NewChecker(permission.ModeDefault, permission.Rules{}, dir),
	})
	var out, errOut bytes.Buffer
	err := Run(context.Background(), a, "count the files", Options{Format: format, Schema: json.RawMessage(answerSchema)}, &out, &errOut)
	return out.String(), p, err
}

func TestStructuredOutput(t *testing.T) {
	out, p, err := runStructured(t, FormatText, "```json\n{ \"count\": 3 }\n```")
	if err != nil || out != "{\"count\":3}\n" {
		t.Fatalf("out %q, err %v", out, err)
	}
	if !strings.Contains(p.prompts[0], "<output-format>") || !strings.Contains(p.prompts[0], `"required":["count"]`) {
		t.Errorf("the model should be told the schema: %q", p.prompts[0])
	}
}

func TestStructuredOutputRetries(t *testing.T) {
	out, p, err := runStructured(t, FormatText, "There are three files.", `{"count":"three"}`, `{"count":3}`)
	if err != nil || out != "{\"count\":3}\n" {
		t.Fatalf("out %q, err %v", out, err)
	}
	if len(p.prompts) != 3 || !strings.Contains(p.prompts[1], "not a JSON value") || !strings.Contains(p.prompts[2], "doesn't match") {
		t.Fatalf("prompts %q", p.prompts)
	}
}

func TestStructuredOutputGivesUp(t *testing.T) {
	out, _, err := runStructured(t, FormatText, "no", "still no", "never")
	if err == nil || !strings.Contains(err.Error(), "--output-schema") || out != "" {
		t.Fatalf("out %q, err %v", out, err)
	}
}

func TestStructuredOutputJSONEvent(t *testing.T) {
	out, _, err := runStructured(t, FormatJSON, `{"count":7}`)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if err != nil || lines[len(lines)-1] != `{"output":{"count":7},"type":"structured_output"}` {
		t.Fatalf("out %q, err %v", out, err)
	}
}
