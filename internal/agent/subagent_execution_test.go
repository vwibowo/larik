package agent

import (
	"testing"

	"larik/internal/config"
	"larik/internal/llm"
	"larik/internal/tools"
)

// namedExecutionProvider needs no live CLI: these tests inspect the child
// registry and model-change policy, never send a provider request.
type namedExecutionProvider struct {
	fakeProvider
	name string
}

func (p *namedExecutionProvider) Name() string { return p.name }

func TestClaudeSubagentsUseToolsIndependentlyOfMain(t *testing.T) {
	cfg := &config.Config{Execution: tools.ExecCode}
	parent := New(Options{Cwd: t.TempDir(), Execution: tools.ExecCode, Tools: tools.Default(), ExecutionFor: cfg.ExecutionFor, SubagentExecutionFor: cfg.SubagentExecutionFor})
	cli := &namedExecutionProvider{name: "claude-code-cli"}
	other := &namedExecutionProvider{name: "openai"}
	spawn := func(a *Agent, p llm.Provider, model string) *Agent {
		return a.Spawn(SpawnOptions{Type: "general-purpose", Provider: p, Model: model, Tools: tools.Default()})
	}
	child := spawn(parent, cli, "opus")
	if child.Execution() != tools.ExecTools || parent.Execution() != tools.ExecCode {
		t.Fatal("child policy changed the main agent or inherited code")
	}
	if _, ok := child.Tools().Get("read"); !ok {
		t.Fatal("CLI child lacks direct read")
	}
	if _, ok := child.Tools().Get(tools.CodeToolName); ok {
		t.Fatal("CLI child still offers run_code")
	}
	if spawn(child, cli, "sonnet").Execution() != tools.ExecTools {
		t.Fatal("nested CLI child inherited wrong mode")
	}
	if spawn(child, other, "m").Execution() != tools.ExecCode {
		t.Fatal("nested non-CLI child lost global code default")
	}
	if spawn(parent, other, "m").Execution() != tools.ExecCode {
		t.Fatal("other provider changed")
	}
	child.SetModel(other, "m", nil)
	if child.Execution() != tools.ExecCode {
		t.Fatal("child model change lost resolution")
	}
	child.SetModel(cli, "sonnet", nil)
	if child.Execution() != tools.ExecTools {
		t.Fatal("child model change back to CLI lost tools default")
	}
	cfg.ModelExecution = map[string]tools.Execution{"claude-code-cli/opus": tools.ExecHybrid}
	if spawn(parent, cli, "opus").Execution() != tools.ExecHybrid {
		t.Fatal("explicit per-model override ignored")
	}
}

func TestClaudeChildExecutionChangePreservesActivePrefix(t *testing.T) {
	cfg := &config.Config{Execution: tools.ExecCode}
	parent := New(Options{Cwd: t.TempDir(), Execution: tools.ExecCode, Tools: tools.Default(), SubagentExecutionFor: cfg.SubagentExecutionFor})
	child := parent.Spawn(SpawnOptions{Type: "general-purpose", Provider: &namedExecutionProvider{name: "claude-code-cli"}, Model: "sonnet", Tools: tools.Default()})
	child.appendMessage(llm.UserText("keep this context"), nil)
	child.SetModel(&namedExecutionProvider{name: "openai"}, "m")
	if child.Execution() != tools.ExecTools {
		t.Fatal("model switch changed active context's tool prefix")
	}
	child.Clear()
	if child.Execution() != tools.ExecCode {
		t.Fatal("new context did not apply child model's execution")
	}
}

func TestClaudeChildDefaultWithoutConfigResolver(t *testing.T) {
	parent := New(Options{Cwd: t.TempDir(), Execution: tools.ExecCode, Tools: tools.Default()})
	child := parent.Spawn(SpawnOptions{Provider: &namedExecutionProvider{name: "claude-code-cli"}, Tools: tools.Default()})
	if child.Execution() != tools.ExecTools {
		t.Fatal("CLI default requires app wiring")
	}
}
