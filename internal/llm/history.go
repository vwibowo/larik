package llm

// RepairToolHistory returns messages with every assistant tool call followed by
// exactly one matching tool result. The repair is request-local: callers can
// safely serialize the returned history without changing the stored transcript.
//
// A malformed history can contain an assistant tool call whose result was lost
// before it reached the agent loop. Providers reject that whole request. Missing
// results are represented as errors so the model can recover and continue.
//
// The opposite also happens: a result whose call is not in the preceding
// assistant message (for example after switching away from a runtime that
// answered its own calls). Providers reject those too, so they are dropped,
// and a user message left empty by that is omitted.
func RepairToolHistory(messages []Message) []Message {
	var out []Message
	for i := 0; i < len(messages); i++ {
		m := messages[i]
		uses := m.ToolUses()
		if m.Role != RoleAssistant || len(uses) == 0 {
			if m.Role == RoleUser {
				if stripped, keep := dropOrphanResults(m); keep {
					out = append(out, stripped)
				}
				continue
			}
			out = append(out, m)
			continue
		}

		out = append(out, m)
		var next Message
		hasUser := i+1 < len(messages) && messages[i+1].Role == RoleUser
		if hasUser {
			next = messages[i+1]
			i++
		} else {
			next.Role = RoleUser
		}

		// Keep the first existing result for each call. Rebuilding the result
		// prefix puts parallel results in call order and before ordinary user
		// content, as required by strict provider APIs.
		existing := make(map[string]Block, len(uses))
		callIDs := make(map[string]bool, len(uses))
		for _, use := range uses {
			callIDs[use.ID] = true
		}
		for _, b := range next.Blocks {
			if b.Type == BlockToolResult && callIDs[b.ID] {
				if _, ok := existing[b.ID]; !ok {
					existing[b.ID] = b
				}
			}
		}

		blocks := make([]Block, 0, len(next.Blocks)+len(uses))
		for _, use := range uses {
			if result, ok := existing[use.ID]; ok {
				blocks = append(blocks, result)
				continue
			}
			blocks = append(blocks, Block{
				Type:    BlockToolResult,
				ID:      use.ID,
				Name:    use.Name,
				Content: "tool call did not produce a result",
				IsError: true,
			})
		}
		for _, b := range next.Blocks {
			if b.Type != BlockToolResult {
				blocks = append(blocks, b)
			}
		}
		next.Blocks = blocks
		out = append(out, next)
	}
	return out
}

// dropOrphanResults removes the tool results of a user message that follows
// no tool call. keep is false when nothing is left. A message without
// results is returned as it is.
func dropOrphanResults(m Message) (Message, bool) {
	has := false
	for _, b := range m.Blocks {
		if b.Type == BlockToolResult {
			has = true
			break
		}
	}
	if !has {
		return m, true
	}
	blocks := make([]Block, 0, len(m.Blocks))
	for _, b := range m.Blocks {
		if b.Type != BlockToolResult {
			blocks = append(blocks, b)
		}
	}
	m.Blocks = blocks
	return m, len(blocks) > 0
}
