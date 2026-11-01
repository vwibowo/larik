package agent

import (
	"cmp"
	"encoding/json"
	"slices"

	"larik/internal/llm"
)

// contextMapItems is how many of the largest tool results a map lists.
const contextMapItems = 5

// ContextPart is one kind of content in the context.
type ContextPart struct {
	Name   string
	Weight int
}

// ContextItem is one tool result that takes up room.
type ContextItem struct {
	Tool   string
	Input  json.RawMessage
	Weight int
}

// ContextMap says what fills the context. Weights are estimates, so a front
// end shows them only as shares of Total. Tokens and Window are the
// provider's measurement, 0 until the next request after a compaction.
type ContextMap struct {
	Parts   []ContextPart // largest first
	Largest []ContextItem // largest first
	Total   int
	Tokens  int
	Window  int
}

// ContextMap breaks down the current context: every tool's calls and results
// count as that tool, and images count apart from what returned them.
func (a *Agent) ContextMap() ContextMap {
	a.mu.Lock()
	defer a.mu.Unlock()
	weights := map[string]int{"system prompt": estimatePrefix(a.opts.System, nil)}
	if reg := a.activeLocked(); reg != nil {
		weights["tool definitions"] = estimatePrefix("", reg.Specs())
	}
	calls := map[string]llm.Block{}
	var largest []ContextItem
	for _, m := range a.messages {
		for _, b := range m.Blocks {
			images := b.Images
			b.Images = nil
			w := estimateMessages([]llm.Message{{Blocks: []llm.Block{b}}})
			weights["images"] += estimateMessages([]llm.Message{{Blocks: []llm.Block{{Images: images}}}})
			switch {
			case b.Type == llm.BlockToolUse:
				calls[b.ID] = b
				weights[b.Name] += w
			case b.Type == llm.BlockToolResult:
				weights[b.Name] += w
				largest = append(largest, ContextItem{Tool: b.Name, Input: calls[b.ID].Input, Weight: w})
			case b.Type == llm.BlockImage:
				weights["images"] += w
			case b.Type == llm.BlockDocument || b.Attachment != "":
				weights["attached files"] += w
			case b.Type == llm.BlockThinking || b.Type == llm.BlockOpaque:
				weights["thinking"] += w
			case m.Role == llm.RoleAssistant:
				weights["replies"] += w
			default:
				weights["your prompts"] += w
			}
		}
	}
	out := ContextMap{Tokens: a.lastContext, Window: a.windowLocked()}
	for name, w := range weights {
		if w > 0 {
			out.Parts = append(out.Parts, ContextPart{name, w})
			out.Total += w
		}
	}
	slices.SortFunc(out.Parts, func(x, y ContextPart) int {
		return cmp.Or(cmp.Compare(y.Weight, x.Weight), cmp.Compare(x.Name, y.Name))
	})
	slices.SortStableFunc(largest, func(x, y ContextItem) int { return cmp.Compare(y.Weight, x.Weight) })
	out.Largest = largest[:min(len(largest), contextMapItems)]
	return out
}
