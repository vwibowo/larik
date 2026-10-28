package agent

import (
	"context"
	"encoding/json"
	"strings"

	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/tools"
)

// maxAskOptions bounds the choices of one question, so the prompt fits
// and the user can pick by number.
const maxAskOptions = 4

// AskUserTool puts a clarifying question to the user. Like exit_plan_mode,
// the question is its permission prompt: permission.Decide always asks for
// it, and authorize writes the user's reply into the input as "answer"
// before Run executes, so the answer is kept with the call on resume.
type AskUserTool struct{}

// ReadOnly: it changes nothing, and a question is as useful in plan mode.
func (AskUserTool) ReadOnly() bool { return true }

func (AskUserTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: permission.AskUserTool,
		Description: "Ask the user a question and wait for the answer, when a choice is genuinely theirs and the request, code or a sensible default can't settle it. " +
			"Not for tool permission or plan approval. Offer up to 4 options, recommendation first; the user can also answer in their own words.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"question":{"type":"string"},
			"options":{"type":"array","maxItems":4,"items":{"type":"object","properties":{
				"label":{"type":"string","description":"A few words"},
				"description":{"type":"string"}},
				"required":["label"]}}},
			"required":["question"]}`),
	}
}

func (AskUserTool) Run(_ context.Context, _ *tools.Env, input json.RawMessage) tools.Result {
	var in struct {
		Answer string `json:"answer"`
	}
	_ = json.Unmarshal(input, &in)
	if answer := strings.TrimSpace(in.Answer); answer != "" {
		return tools.Result{Content: "The user answered: " + answer}
	}
	return tools.Result{Content: "The user gave no answer. Proceed with your best judgment and say what you assumed."}
}

// AskOption is one choice offered with a question.
type AskOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// QuestionOf returns the question of an ask_user input.
func QuestionOf(input json.RawMessage) string {
	var in struct {
		Question string `json:"question"`
	}
	_ = json.Unmarshal(input, &in)
	return strings.TrimSpace(in.Question)
}

// OptionsOf returns the usable options of an ask_user input: labelled,
// without repeats, and at most maxAskOptions.
func OptionsOf(input json.RawMessage) []AskOption {
	var in struct {
		Options []AskOption `json:"options"`
	}
	_ = json.Unmarshal(input, &in)
	var out []AskOption
	seen := map[string]bool{}
	for _, o := range in.Options {
		o.Label, o.Description = strings.TrimSpace(o.Label), strings.TrimSpace(o.Description)
		if o.Label == "" || seen[o.Label] {
			continue
		}
		seen[o.Label] = true
		out = append(out, o)
		if len(out) == maxAskOptions {
			break
		}
	}
	return out
}

// withAnswer returns input with "answer" set to the user's reply,
// replacing any the model put there itself.
func withAnswer(input json.RawMessage, answer string) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(input, &m) != nil || m == nil {
		m = map[string]json.RawMessage{}
	}
	a, _ := json.Marshal(strings.TrimSpace(answer))
	m["answer"] = a
	out, err := json.Marshal(m)
	if err != nil {
		return input
	}
	return out
}
