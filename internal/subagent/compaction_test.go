package subagent

import (
	"context"
	"iter"
	"strings"
	"sync"
	"testing"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/permission"
)

// childCompactingProvider fills the subagent's context once: the child's
// second request overflows, so the child compacts mid-turn the way a
// long-running one does. The parent is answered normally. Overflowing is
// used rather than a huge transcript so the test doesn't depend on how big
// a child's system prompt happens to be.
type childCompactingProvider struct {
	mu        sync.Mutex
	childReqs int
	compacted int
}

func (p *childCompactingProvider) Name() string { return "fake" }

func (p *childCompactingProvider) Stream(ctx context.Context, req llm.Request) iter.Seq2[llm.StreamEvent, error] {
	reply, err := p.next(req)
	return func(yield func(llm.StreamEvent, error) bool) {
		if err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		reply.Role, reply.Model = llm.RoleAssistant, req.Model
		stop := llm.StopEnd
		if len(reply.ToolUses()) > 0 {
			stop = llm.StopToolUse
		}
		yield(llm.StreamEvent{Type: llm.EventDone, Message: reply, StopReason: stop, Usage: llm.Usage{Input: 10, Output: 5}}, nil)
	}
}

func (p *childCompactingProvider) next(req llm.Request) (llm.Message, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !isChild(req) {
		if len(toolResults(req)) > 0 {
			return text("ok"), nil
		}
		return llm.Message{Blocks: []llm.Block{use("p1", "task", `{"description":"find auth","prompt":"Where is auth?","subagent_type":"explore"}`)}}, nil
	}
	if last := req.Messages[len(req.Messages)-1]; strings.Contains(last.Text(), "Summarize the transcript inside") {
		p.compacted++
		return text("<summary>the child searched for auth</summary>"), nil
	}
	p.childReqs++
	switch p.childReqs {
	case 1:
		return llm.Message{Blocks: []llm.Block{use("c1", "grep", `{"pattern":"package"}`)}}, nil
	case 2:
		return llm.Message{}, llm.ErrContextOverflow
	default:
		return text("auth is in auth.go"), nil
	}
}

// A subagent that compacts its own context says so, labelled — the way its
// compaction *failure* notice already was. Unlabelled it would read as the
// main conversation having been summarized, and counted as the parent's it
// would misreport both contexts.
func TestASubagentsCompactionIsForwardedWithItsLabel(t *testing.T) {
	p := &childCompactingProvider{}
	a, _, _ := newParent(t, p, permission.ModeDefault)

	evs := drain(a.Run(context.Background(), "find auth"), true)

	var labelled, unlabelled int
	var line agent.Event
	for _, e := range evs {
		switch {
		case e.Kind != agent.EvCompacted:
		case e.Agent == "":
			unlabelled++
		default:
			labelled, line = labelled+1, e
		}
	}
	if p.compacted != 1 {
		t.Fatalf("the child compacted %d times, want 1", p.compacted)
	}
	if labelled != 1 || unlabelled != 0 {
		t.Fatalf("forwarded compactions: %d labelled, %d unlabelled", labelled, unlabelled)
	}
	if !strings.Contains(line.Agent, "find auth") || line.Model == "" {
		t.Errorf("the compaction should name the subagent and its model: agent=%q model=%q", line.Agent, line.Model)
	}
	if line.Compaction == nil || line.Compaction.Trigger != "overflow" {
		t.Errorf("compaction metrics: %+v", line.Compaction)
	}

	st := a.Stats()
	if st.Compactions != 0 {
		t.Errorf("the parent recorded %d compactions of its own; only the child compacted", st.Compactions)
	}
	if st.Delegated.Input == 0 {
		t.Error("the child's spend, its summary request included, should count as delegated")
	}
}
