package agent

import (
	"hash/fnv"

	"larik/internal/llm"
)

// A subagent is stuck when the same tool calls with the same results
// recur this often within the last loopWindow turns. Matching results too
// keeps honest retries apart: rerunning tests after an edit gives
// different output.
const (
	loopWindow  = 8
	loopRepeats = 4
)

// loopGuard notices a subagent repeating itself, which small models do
// when they lose track of the task, and which nobody is watching.
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
