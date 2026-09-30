//go:build windows

package claudeagent

import (
	"context"
	"os"
	"strings"
	"testing"

	"larik/internal/llm"
)

func TestRuntimeParserAndMissingCLI(t *testing.T) {
	out := make(chan llm.AgentRuntimeEvent, 2)
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}` + "\n"
	if err := parseOutput(context.Background(), strings.NewReader(line), out, &callMatcher{}, 1); err != nil {
		t.Fatal(err)
	}
	if event := <-out; event.Assistant == nil || event.Assistant.Text() != "hello" {
		t.Fatalf("assistant event = %+v", event)
	}
	runtime := &Runtime{LookupPath: func(string) (string, error) { return "", os.ErrNotExist }}
	if got := runtime.Check(context.Background()); got.State != string(StateMissing) {
		t.Fatalf("missing status = %+v", got)
	}
}
