package controller

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

const xKeywordSearchToolName = "x_keyword_search"

// normalizeACPToolDisplayUpdate converts recognized provider result shapes
// into explicit Caelis display metadata. ACP rawOutput remains tool-owned
// result data and must not be interpreted generically as invocation input.
func normalizeACPToolDisplayUpdate(update client.Update) client.Update {
	switch typed := update.(type) {
	case client.ToolCall:
		typed.Meta = client.WithoutDisplayToolInput(typed.Meta)
		return typed
	case client.ToolCallUpdate:
		typed.Meta = client.WithoutDisplayToolInput(typed.Meta)
		if !strings.EqualFold(strings.TrimSpace(derefString(typed.Status)), eventstream.ToolStatusCompleted) {
			return typed
		}
		input := delayedXSearchDisplayInput(typed.RawOutput)
		if len(input) == 0 {
			return typed
		}
		typed.Meta = client.WithDisplayToolInput(typed.Meta, input)
		return typed
	default:
		return update
	}
}

func delayedXSearchDisplayInput(rawOutput any) map[string]any {
	output, ok := rawOutput.(map[string]any)
	if !ok || !strings.EqualFold(strings.TrimSpace(stringMapValue(output, "name")), xKeywordSearchToolName) {
		return nil
	}
	serialized, ok := output["input"].(string)
	if !ok || strings.TrimSpace(serialized) == "" {
		return nil
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(serialized), &decoded); err != nil {
		return nil
	}
	query, ok := decoded["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return nil
	}
	return map[string]any{"query": query}
}
