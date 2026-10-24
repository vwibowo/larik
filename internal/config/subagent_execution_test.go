package config

import (
	"larik/internal/tools"
	"testing"
)

func TestSubagentExecution(t *testing.T) {
	cfg := &Config{Execution: tools.ExecCode}
	for _, model := range []string{"opus", "sonnet", "claude-opus-5"} {
		if got := cfg.SubagentExecutionFor("claude-code-cli", model); got != tools.ExecTools {
			t.Errorf("CLI %s: %s", model, got)
		}
		if got := cfg.ExecutionFor("claude-code-cli", model); got != tools.ExecCode {
			t.Errorf("main changed: %s", got)
		}
	}
	if got := cfg.SubagentExecutionFor("openai", "m"); got != tools.ExecCode {
		t.Errorf("non-CLI changed: %s", got)
	}
	cfg.ModelExecution = map[string]tools.Execution{"claude-code-cli/opus": tools.ExecHybrid, "sonnet": tools.ExecCode}
	if got := cfg.SubagentExecutionFor("claude-code-cli", "opus"); got != tools.ExecHybrid {
		t.Errorf("qualified override lost: %s", got)
	}
	if got := cfg.SubagentExecutionFor("claude-code-cli", "sonnet"); got != tools.ExecCode {
		t.Errorf("bare override lost: %s", got)
	}
}
