package agent

import "larik/internal/llm"

// Larik never shows an estimated token count. Every number a user sees —
// the footer, /cost, the compaction savings — comes from a provider's own
// usage report. The estimates here exist for exactly one decision: whether
// to compact before sending a request whose size the last measurement
// cannot account for, such as a huge pasted prompt, a long tool result, or
// a resumed transcript that has never been measured at all.
const (
	// charsPerToken is the usual English approximation. It errs low on
	// code, and that direction is deliberate: an estimate that is too
	// eager would compact a conversation that still fits, which throws
	// work away, while one that is too cautious merely falls back on the
	// provider's own context-overflow error and a retry.
	charsPerToken = 4
	// imageTokens stands in for an image block. An image costs roughly a
	// fixed amount whatever its byte size, so measuring its base64
	// payload as if it were text would overstate one screenshot by two
	// orders of magnitude and compact a conversation that is nearly empty.
	imageTokens = 1600
	// documentTokensPerPage stands in for one page of an attached
	// document. A provider charges for a PDF page roughly as it would for
	// a page of text plus a picture of it, which runs to a few thousand
	// tokens however many bytes the page occupies — a scanned page is
	// twenty times the size of a typed one and costs about the same. This
	// is the low end of that range, erring low as the rest of this file
	// does.
	documentTokensPerPage = 1500
)

// estimatePrefix approximates the part of every request that does not
// change within a context: the system prompt and the tool definitions.
// Compaction never touches it.
func estimatePrefix(system string, specs []llm.ToolSpec) int {
	chars := len(system)
	for _, s := range specs {
		chars += len(s.Name) + len(s.Description) + len(s.Schema)
	}
	return chars / charsPerToken
}

// estimateMessages approximates the conversation, which is the part
// compaction replaces.
func estimateMessages(msgs []llm.Message) int {
	chars, tokens := 0, 0
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockImage {
				tokens += imageTokens
				continue
			}
			if b.Type == llm.BlockDocument {
				tokens += documentTokensPerPage * max(b.Pages, 1)
				continue
			}
			chars += len(b.Text) + len(b.Signature) + len(b.Content) +
				len(b.Name) + len(b.Input) + len(b.Raw)
			for _, img := range b.Images { // images and documents a tool returned
				if img.Type == llm.BlockDocument {
					tokens += documentTokensPerPage * max(img.Pages, 1)
				} else {
					tokens += imageTokens
				}
			}
		}
	}
	return tokens + chars/charsPerToken
}

// prefixEstimateLocked is estimatePrefix for the current system prompt and
// tool set, recomputed only when either changes. Callers hold a.mu.
func (a *Agent) prefixEstimateLocked() int {
	reg := a.activeLocked()
	if a.prefixEstFor.system == a.opts.System && a.prefixEstFor.tools == reg {
		return a.prefixEst
	}
	var specs []llm.ToolSpec
	if reg != nil {
		specs = reg.Specs()
	}
	a.prefixEst = estimatePrefix(a.opts.System, specs)
	a.prefixEstFor.system, a.prefixEstFor.tools = a.opts.System, reg
	return a.prefixEst
}

// messageEstimateLocked is estimateMessages(a.messages), kept as a running
// sum so a long turn does not re-measure the whole transcript every step.
// The sum covers every message but the last, which can still grow in
// place. A transcript that was replaced (compaction, a resume) or cut back
// and appended to again (a rewind) no longer holds the messages the sum
// counted at the same addresses, which resets it. Callers hold a.mu.
func (a *Agent) messageEstimateLocked() int {
	n := len(a.messages)
	if n == 0 {
		return 0
	}
	c := &a.msgEst
	if c.first != &a.messages[0] || c.n > n-1 || (c.n > 0 && !c.last.same(a.messages[c.n-1])) {
		*c = messageEstimate{first: &a.messages[0]}
	}
	c.sum += estimateMessages(a.messages[c.n : n-1])
	c.n = n - 1
	if c.n > 0 {
		c.last = blocksOf(a.messages[c.n-1])
	}
	return c.sum + estimateMessages(a.messages[n-1:])
}

// blockRef identifies a message's block slice, which a different message
// at the same index would not share.
type blockRef struct {
	first *llm.Block
	n     int
}

func blocksOf(m llm.Message) blockRef {
	if len(m.Blocks) == 0 {
		return blockRef{}
	}
	return blockRef{&m.Blocks[0], len(m.Blocks)}
}

func (r blockRef) same(m llm.Message) bool { return r == blocksOf(m) }

// messageEstimate is the running sum behind messageEstimateLocked.
type messageEstimate struct {
	first *llm.Message // a.messages[0] when the sum was taken
	n     int          // messages counted in sum
	last  blockRef     // the last counted message's blocks
	sum   int
}
