package llm

// RepairToolHistory returns messages with every assistant tool call followed by
// exactly one matching tool result. The repair is request-local: callers can
// safely serialize the returned history without changing the stored transcript.
//
// A malformed history can contain an assistant tool call whose result was lost
// before it reached the agent loop. Providers reject that whole request. Missing
// results are represented as errors so the model can recover and continue.
func RepairToolHistory(messages []Message) []Message {
	var out []Message
	for i := 0; i < len(messages); i++ {
		m := messages[i]
		uses := m.ToolUses()
		if m.Role != RoleAssistant || len(uses) == 0 {
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
			if b.Type != BlockToolResult || !callIDs[b.ID] {
				blocks = append(blocks, b)
			}
		}
		next.Blocks = blocks
		out = append(out, next)
	}
	return out
}
