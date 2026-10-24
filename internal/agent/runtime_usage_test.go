package agent

import (
	"context"
	"testing"

	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/session"
	"larik/internal/tools"
)

// usageRuntime answers every pass with a reply and the pass's usage.
type usageRuntime struct {
	usage llm.Usage
	runs  *int
}

func (r usageRuntime) Run(ctx context.Context, req llm.AgentRuntimeRequest) (<-chan llm.AgentRuntimeEvent, error) {
	if r.runs != nil {
		*r.runs++
	}
	out := make(chan llm.AgentRuntimeEvent, 3)
	reply := llm.Message{Role: llm.RoleAssistant, Model: req.Model, Blocks: []llm.Block{llm.TextBlock("ok")}}
	u := r.usage
	out <- llm.AgentRuntimeEvent{Assistant: &reply}
	out <- llm.AgentRuntimeEvent{Usage: &u}
	out <- llm.AgentRuntimeEvent{Done: true}
	close(out)
	return out, nil
}

func TestRuntimeEntryIsAdmittedByTheBudget(t *testing.T) {
	runs := 0
	dir := t.TempDir()
	a := New(Options{Provider: &fakeProvider{}, Runtime: usageRuntime{runs: &runs}, Model: "sonnet", Cwd: dir, Tools: tools.NewRegistry(),
		Perms: permission.NewChecker(permission.ModeYolo, permission.Rules{}, dir)})
	tokenCapped(a, 100)

	var stop string
	for _, e := range drain(a.Run(context.Background(), "go"), PermissionReply{}) {
		if e.Kind == EvDone {
			stop = e.StopReason
		}
	}
	if stop != "budget" || runs != 0 {
		t.Fatalf("stop %q, runtime runs %d; want the budget to stop it before it starts", stop, runs)
	}
}

// A whole-turn runtime's spend is saved with the session, once, so a
// resumed session counts it, and adds to it without counting it twice.
func TestRuntimeUsageIsSavedOnceAndSurvivesResume(t *testing.T) {
	use := llm.Usage{Input: 100, Output: 20, CacheRead: 30}
	a, _, dir := setup(t, permission.ModeYolo)
	a.opts.Runtime = usageRuntime{usage: use}
	drain(a.Run(context.Background(), "first"), PermissionReply{})

	st, err := session.Load(a.SessionPath())
	if err != nil {
		t.Fatal(err)
	}
	if st.Usage != use || st.ByModel["m"] != use {
		t.Fatalf("saved usage %+v (by model %+v), want %+v once", st.Usage, st.ByModel, use)
	}

	b := New(Options{Provider: &fakeProvider{}, Runtime: usageRuntime{usage: use}, Model: "m", Cwd: dir, Tools: tools.NewRegistry(),
		Perms: permission.NewChecker(permission.ModeYolo, permission.Rules{}, dir), Session: a.opts.Session})
	b.Restore(st)
	if got := b.Stats().Total; got != use {
		t.Fatalf("resumed total %+v, want %+v", got, use)
	}
	drain(b.Run(context.Background(), "second"), PermissionReply{})

	twice := use
	twice.Add(use)
	if got := b.Stats().Total; got != twice {
		t.Errorf("total after resume %+v, want %+v", got, twice)
	}
	st, err = session.Load(a.SessionPath())
	if err != nil {
		t.Fatal(err)
	}
	if st.Usage != twice {
		t.Errorf("saved usage after resume %+v, want %+v", st.Usage, twice)
	}
}
