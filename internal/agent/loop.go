package agent

import (
	"fmt"
	"hash/fnv"

	"larik/internal/llm"
	"larik/internal/permission"
)

// An agent is stuck when the same tool calls with the same results recur
// this often within the last loopWindow turns. Matching results too keeps
// honest retries apart: rerunning tests after an edit gives different
// output.
const (
	loopWindow  = 8
	loopRepeats = 4
)

// loopGuard notices an agent repeating itself, which small models do when
// they lose track of the task. It guards the root agent as well as
// subagents; what differs is the response, which loopStop decides.
type loopGuard struct{ recent []uint64 }

// see records one turn's tool calls and results and reports whether that
// exact turn has now happened loopRepeats times within the window.
func (g *loopGuard) see(calls []llm.Block, results []llm.Block) bool {
	h := fnv.New64a()
	for _, c := range calls {
		h.Write([]byte(c.Name))
		h.Write([]byte{0})
		h.Write(c.Input)
		h.Write([]byte{0})
	}
	for _, r := range results {
		h.Write([]byte(r.Content))
		h.Write([]byte{0})
	}
	sig := h.Sum64()
	g.recent = append(g.recent, sig)
	if len(g.recent) > loopWindow {
		g.recent = g.recent[len(g.recent)-loopWindow:]
	}
	n := 0
	for _, s := range g.recent {
		if s == sig {
			n++
		}
	}
	return n >= loopRepeats
}

// loopStop is called when the guard trips on a call to tool name and
// reports whether the turn should end. A subagent, an unattended run and
// an auto or yolo session have nobody to press Esc, so they stop. In an
// ordinary interactive session the user is watching: say so once per turn
// and carry on.
func (a *Agent) loopStop(name string, warned *bool, emit func(Event)) bool {
	who := "the agent"
	if a.opts.Subagent != "" {
		who = "the subagent"
	}
	if a.opts.Subagent != "" || a.opts.Unattended || a.autonomous() {
		emit(Event{Kind: EvError, Text: fmt.Sprintf("stopped: %s repeated the same %s call with the same result %d times; it looks stuck", who, name, loopRepeats)})
		return true
	}
	if !*warned {
		*warned = true
		emit(Event{Kind: EvNotice, Text: fmt.Sprintf("%s repeated the same %s call with the same result %d times; press Esc if it is stuck", who, name, loopRepeats)})
	}
	return false
}

// autonomous reports a permission mode that runs tools without asking the
// user each time.
func (a *Agent) autonomous() bool {
	if a.opts.Perms == nil {
		return false
	}
	m := a.opts.Perms.Mode()
	return m == permission.ModeAuto || m == permission.ModeYolo
}
