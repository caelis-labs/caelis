package runtime

import "github.com/caelis-labs/caelis/agent-sdk/model"

// compactionPrefixSafe reports whether messages contain complete, contiguous
// tool batches, so a final user compaction instruction can safely follow them.
// Call IDs are unique within a batch and may be reused after it closes.
func compactionPrefixSafe(messages []model.Message) bool {
	pending := make(map[string]struct{})
	for _, message := range messages {
		var calls, results int
		for _, part := range message.Parts {
			if part.ToolUse != nil {
				calls++
			}
			if part.ToolResult != nil {
				results++
			}
		}

		if calls > 0 {
			if message.Role != model.RoleAssistant || results > 0 || len(pending) > 0 {
				return false
			}
			for _, part := range message.Parts {
				if part.ToolUse == nil {
					continue
				}
				id := part.ToolUse.ID
				if _, exists := pending[id]; id == "" || exists {
					return false
				}
				pending[id] = struct{}{}
			}
			continue
		}

		if results > 0 {
			if message.Role != model.RoleTool {
				return false
			}
			for _, part := range message.Parts {
				if part.ToolResult == nil {
					continue
				}
				id := part.ToolResult.ToolUseID
				if _, exists := pending[id]; !exists {
					return false
				}
				delete(pending, id)
			}
			continue
		}

		if len(pending) > 0 {
			return false
		}
	}
	return len(pending) == 0
}
