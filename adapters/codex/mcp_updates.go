package codex

import (
	"encoding/json"

	acp "github.com/caelis-labs/acp-go-sdk"
)

// Codex supplies MCP content blocks inside result.content. ACP tool content is
// the presentation source; rawOutput retains the complete provider result.
func mcpToolContent(item threadItem) []acp.ToolCallContent {
	if item.Type != "mcpToolCall" {
		return nil
	}
	if failure, ok := item.Error.(map[string]any); ok {
		if message, ok := failure["message"].(string); ok && message != "" {
			return []acp.ToolCallContent{acp.ToolContent(acp.TextBlock(message))}
		}
	}
	result, ok := item.Result.(map[string]any)
	if !ok {
		return nil
	}
	blocks, ok := result["content"].([]any)
	if !ok {
		return nil
	}
	content := make([]acp.ToolCallContent, 0, len(blocks))
	for _, raw := range blocks {
		encoded, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		var block acp.ContentBlock
		if json.Unmarshal(encoded, &block) == nil {
			content = append(content, acp.ToolContent(block))
		}
	}
	return content
}

// This provider-owned display hint retains structured MCP provenance without
// assigning a client-specific tool profile or inferring identity from a title.
func mcpToolMeta(item threadItem) map[string]json.RawMessage {
	if item.Type != "mcpToolCall" || item.Server == "" || item.Tool == "" {
		return nil
	}
	return encodeMeta(map[string]any{"codex/mcp_tool": map[string]any{"server": item.Server, "name": item.Tool}})
}
