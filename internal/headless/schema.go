package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"larik/internal/agent"
	"larik/internal/tools"
)

// Options configures a headless run.
type Options struct {
	Format Format
	// Schema, when set, is the JSON Schema the final answer must match.
	Schema json.RawMessage
}

// schemaRetries is how many times the model may correct an answer that
// doesn't match the schema.
const schemaRetries = 2

func schemaNote(schema json.RawMessage) string {
	return "\n\n<output-format>When you are done, your final message must be only a JSON value matching this JSON Schema: " +
		"no prose and no code fences.\n" + string(schema) + "\n</output-format>"
}

// finishStructured checks the final answer against schema, letting the
// model correct it, and writes the value.
func (p *printer) finishStructured(ctx context.Context, a *agent.Agent, schema json.RawMessage, taskDone <-chan struct{}) error {
	for try := 0; ; try++ {
		p.mu.Lock()
		answer := p.last
		p.mu.Unlock()
		value, err := answerJSON(answer)
		if err == nil {
			err = tools.ValidateJSON(schema, value)
		}
		if err == nil {
			p.writeStructured(value)
			return nil
		}
		if try == schemaRetries {
			return fmt.Errorf("the final answer doesn't match --output-schema: %v", err)
		}
		fix := "Your final answer doesn't match the required JSON Schema: " + err.Error() +
			". Reply with only the corrected JSON value, no prose or code fences."
		if failure := p.run(ctx, a, fix, taskDone); failure != "" {
			return ErrReported{failure}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

// answerJSON takes the JSON value out of an answer, allowing for a code
// fence around it.
func answerJSON(answer string) (json.RawMessage, error) {
	s := strings.TrimSpace(answer)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
		s = strings.TrimSpace(s)
	}
	if s == "" {
		return nil, fmt.Errorf("the answer is empty")
	}
	var out bytes.Buffer
	if err := json.Compact(&out, []byte(s)); err != nil {
		return nil, fmt.Errorf("the answer is not a JSON value: %v", err)
	}
	return out.Bytes(), nil
}

func (p *printer) writeStructured(value json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.format == FormatJSON {
		_ = p.enc.Encode(map[string]any{"type": "structured_output", "output": value})
		return
	}
	fmt.Fprintln(p.stdout, string(value))
}
