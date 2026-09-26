package skills

import (
	"context"
	"encoding/json"

	"larik/internal/llm"
	"larik/internal/tools"
)

// Tool lets the model load a skill's full instructions by name.
type Tool struct{ Set *Set }

func (Tool) ReadOnly() bool { return true }

func (Tool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "skill",
		Description: "Load a skill's full instructions by name (see the skills list in the system prompt). Call it before starting a task the skill covers.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"name":{"type":"string","description":"Skill name"},
			"arguments":{"type":"string","description":"Optional arguments for the skill"}},
			"required":["name"]}`),
	}
}

func (t Tool) Run(ctx context.Context, _ *tools.Env, input json.RawMessage) tools.Result {
	var in struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if len(input) == 0 || json.Unmarshal(input, &in) != nil {
		return tools.Result{Content: "INVALID_JSON: expected {\"name\": ...}", IsError: true}
	}
	sk, body, err := t.Set.Body(in.Name)
	if err != nil {
		return tools.Result{Content: err.Error(), IsError: true}
	}
	return tools.Result{Content: tools.Truncate(Render(sk, body, in.Arguments), tools.MaxOutputBytes*2)}
}
