package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/session"
	"larik/internal/tools"
)

// ToolName is the name the model uses to delegate.
const ToolName = "task"

const childMaxTurns = 100

// Resolver turns a model spec ("provider/model" or bare id) into a provider.
type Resolver func(spec string) (llm.Provider, string, error)

var aliases = map[string]string{
	"opus":   "claude-opus-5",
	"sonnet": "claude-sonnet-5",
	"haiku":  "claude-haiku-4-5",
}

// Tool is the task tool. It is ReadOnly because the call itself changes
// nothing: every tool call the child makes is permission-checked on its
// own. That also lets independent tasks run in parallel.
type Tool struct {
	Set     *Set
	Resolve Resolver
	// Context is appended to every child system prompt (env block and
	// project instructions, plus the skills index when available).
	Context string
}

func (t *Tool) ReadOnly() bool { return true }

func (t *Tool) Spec() llm.ToolSpec {
	var names []string
	var b strings.Builder
	b.WriteString("Delegate a task to a subagent with its own fresh context window. Use it for broad searches, multi-step research, " +
		"or self-contained changes whose details you don't need to keep in your own context. The subagent cannot see this conversation: " +
		"give it a complete, self-contained prompt with all needed context and say exactly what to report back. Its final message is returned to you. " +
		"For independent work, call task several times in the same turn to run subagents in parallel; don't parallelize tasks that edit the same files.\n\nAvailable agents:\n")
	for _, d := range t.Set.List() {
		names = append(names, d.Name)
		fmt.Fprintf(&b, "- %s: %s", d.Name, d.Description)
		if d.Tools != nil {
			fmt.Fprintf(&b, " (tools: %s)", strings.Join(d.Tools, ", "))
		}
		b.WriteString("\n")
	}
	enum, _ := json.Marshal(names)
	return llm.ToolSpec{
		Name:        ToolName,
		Description: strings.TrimSpace(b.String()),
		Schema: json.RawMessage(`{"type":"object","properties":{
			"description":{"type":"string","description":"Short (3-5 word) label for the task"},
			"prompt":{"type":"string","description":"Complete instructions for the subagent"},
			"subagent_type":{"type":"string","enum":` + string(enum) + `,"description":"Which agent to use"}},
			"required":["description","prompt","subagent_type"]}`),
	}
}

func (t *Tool) Run(ctx context.Context, _ *tools.Env, input json.RawMessage) tools.Result {
	var in struct {
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
		Type        string `json:"subagent_type"`
	}
	if len(input) == 0 || json.Unmarshal(input, &in) != nil || strings.TrimSpace(in.Prompt) == "" {
		return tools.Result{Content: "INVALID_JSON: expected description, prompt and subagent_type", IsError: true}
	}
	parent, emit, ok := agent.FromContext(ctx)
	if !ok {
		return tools.Result{Content: "task tool: no parent agent in context", IsError: true}
	}
	def, ok := t.Set.Get(in.Type)
	if !ok {
		return tools.Result{Content: fmt.Sprintf("unknown subagent_type %q", in.Type), IsError: true}
	}

	provider, model, notice := t.model(def, parent)
	if notice != "" {
		emit(agent.Event{Kind: agent.EvNotice, Text: notice})
	}

	label := def.Name
	if d := strings.TrimSpace(in.Description); d != "" {
		label += ": " + d
	}
	var sess *session.Session
	if p := parent.SessionPath(); p != "" {
		dir := filepath.Join(filepath.Dir(p), strings.TrimSuffix(filepath.Base(p), ".jsonl")+"-agents")
		sess, _ = session.Create(dir, session.Meta{Cwd: parent.Cwd(), Provider: provider.Name(), Model: model})
		if sess != nil {
			defer sess.Close()
		}
	}

	child := parent.Spawn(agent.SpawnOptions{
		Type:     def.Name,
		Provider: provider,
		Model:    model,
		System:   def.Prompt + "\n\n" + footer + "\n\n" + t.Context,
		Tools:    childTools(parent.Tools(), def),
		Session:  sess,
		MaxTurns: childMaxTurns,
	})

	var final, failure, stop string
	calls := 0
	for e := range child.Run(ctx, in.Prompt) {
		switch e.Kind {
		case agent.EvToolStart, agent.EvToolEnd, agent.EvPermission, agent.EvNotice:
			if e.Kind == agent.EvToolEnd {
				calls++
			}
			e.Agent = label
			emit(e)
		case agent.EvError:
			failure = e.Text
			e.Agent = label
			emit(e)
		case agent.EvUsage:
			parent.AddUsage(model, e.Usage.Turn)
			st := parent.Stats()
			emit(agent.Event{Kind: agent.EvUsage, Usage: &st})
		case agent.EvAssistant:
			if text := strings.TrimSpace(e.Message.Text()); text != "" {
				final = text
			}
		case agent.EvDone:
			stop = e.StopReason
		}
	}

	switch {
	case ctx.Err() != nil:
		return tools.Result{Content: "subagent interrupted by user", IsError: true}
	case failure != "" && final == "":
		return tools.Result{Content: "subagent failed: " + failure, IsError: true}
	case final == "":
		return tools.Result{Content: fmt.Sprintf("subagent finished (%s) without a final message after %d tool calls", stop, calls), IsError: true}
	}
	return tools.Result{Content: tools.Truncate(final, tools.MaxOutputBytes)}
}

// model picks the child's provider and model from the definition.
func (t *Tool) model(def Definition, parent *agent.Agent) (llm.Provider, string, string) {
	spec := def.Model
	if spec == "" || spec == "inherit" || t.Resolve == nil {
		return parent.Provider(), parent.Model(), ""
	}
	if id, ok := aliases[spec]; ok {
		if parent.ProviderName() != "anthropic" {
			return parent.Provider(), parent.Model(), fmt.Sprintf("agent %s asks for model %q, a Claude alias; using %s", def.Name, spec, parent.Model())
		}
		spec = "anthropic/" + id
	}
	p, m, err := t.Resolve(spec)
	if err != nil {
		return parent.Provider(), parent.Model(), fmt.Sprintf("agent %s: model %q unavailable (%v); using %s", def.Name, spec, err, parent.Model())
	}
	return p, m, ""
}

// childTools is the parent's registry filtered by the definition, minus
// task itself (no nested delegation).
func childTools(parent *tools.Registry, def Definition) *tools.Registry {
	var out []tools.Tool
	for _, spec := range parent.Specs() {
		if spec.Name == ToolName || !def.toolAllowed(spec.Name) {
			continue
		}
		if tl, ok := parent.Get(spec.Name); ok {
			out = append(out, tl)
		}
	}
	return tools.NewRegistry(out...)
}
