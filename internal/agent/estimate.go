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
			chars += len(b.Text) + len(b.Signature) + len(b.Content) +
				len(b.Name) + len(b.Input) + len(b.Raw)
			tokens += imageTokens * len(b.Images) // images a tool returned
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
