package controller

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

const xKeywordSearchToolName = "x_keyword_search"

// normalizeACPToolDisplayUpdate converts recognized provider result shapes
// into explicit Caelis display metadata. ACP rawOutput remains tool-owned
// result data and must not be interpreted generically as invocation input.
func normalizeACPToolDisplayUpdate(update client.Update) client.Update {
	switch typed := update.(type) {
	case client.ToolCall:
		typed.Meta = metautil.WithoutSectionKeys(typed.Meta, metautil.Display, metautil.DisplayToolInput)
		return typed
	case client.ToolCallUpdate:
		typed.Meta = metautil.WithoutSectionKeys(typed.Meta, metautil.Display, metautil.DisplayToolInput)
		if !strings.EqualFold(strings.TrimSpace(derefString(typed.Status)), schema.ToolStatusCompleted) {
			return typed
		}
		input := delayedXSearchDisplayInput(typed.RawOutput)
		if len(input) == 0 {
			return typed
		}
		typed.Meta = metautil.WithSection(typed.Meta, metautil.Display, map[string]any{
			metautil.DisplayToolInput: input,
		})
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
