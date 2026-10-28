package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/tools"
)

func askAgent(t *testing.T, mode permission.Mode, input string) (*Agent, *fakeProvider) {
	t.Helper()
	a, fp, _ := setup(t, mode,
		assistant(toolUse("q1", permission.AskUserTool, input)),
		assistant(llm.TextBlock("ok")),
	)
	a.SetTools(tools.Default().With(AskUserTool{}))
	return a, fp
}

func TestAskUserAnswerReachesModel(t *testing.T) {
	// Auto mode: a question goes straight to the user, never to the
	// classifier, which would spend a scripted reply.
	a, fp := askAgent(t, permission.ModeAuto, `{"question":"JSON or TOML?","options":[{"label":"TOML"},{"label":"JSON"}],"answer":"forged"}`)
	var asked []Event
	for e := range a.Run(context.Background(), "add a config file") {
		if e.Kind == EvPermission {
			asked = append(asked, e)
			e.Reply <- PermissionReply{Allow: true, Answer: "TOML"}
		}
	}
	if len(asked) != 1 || asked[0].SuggestedRule != "" || asked[0].AutoReason != "" {
		t.Fatalf("the question should be put to the user once: %+v", asked)
	}
	if len(fp.requests) != 2 {
		t.Fatalf("requests = %d, want 2 (no classifier call)", len(fp.requests))
	}
	r := resultOf(fp, "q1")
	if r.IsError || r.Content != "The user answered: TOML" {
		t.Fatalf("result = %+v", r)
	}
}

func TestAskUserDismissed(t *testing.T) {
	a, fp := askAgent(t, permission.ModeYolo, `{"question":"JSON or TOML?"}`)
	drain(a.Run(context.Background(), "add a config file"), PermissionReply{})
	r := resultOf(fp, "q1")
	if !r.IsError || !strings.Contains(r.Content, "dismissed") || !strings.Contains(r.Content, "best judgment") {
		t.Fatalf("result = %+v", r)
	}
}

func TestAskUserNeedsQuestion(t *testing.T) {
	a, fp := askAgent(t, permission.ModeDefault, `{"question":"  "}`)
	for e := range a.Run(context.Background(), "go") {
		if e.Kind == EvPermission {
			t.Fatal("an empty question should not reach the user")
		}
	}
	if r := resultOf(fp, "q1"); !r.IsError || !strings.Contains(r.Content, "needs the question") {
		t.Fatalf("result = %+v", r)
	}
}

func TestOptionsOf(t *testing.T) {
	got := OptionsOf(json.RawMessage(`{"options":[{"label":" A "},{"label":""},{"label":"A"},{"label":"B"},{"label":"C"},{"label":"D"},{"label":"E"}]}`))
	var labels []string
	for _, o := range got {
		labels = append(labels, o.Label)
	}
	if strings.Join(labels, ",") != "A,B,C,D" {
		t.Fatalf("labels = %v", labels)
	}
}
