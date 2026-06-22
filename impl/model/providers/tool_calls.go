package providers

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/model"
)

func dedupToolCalls(calls []model.ToolCall) []model.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	index := map[string]int{}
	out := make([]model.ToolCall, 0, len(calls))
	for _, call := range calls {
		key := callKey(call)
		if pos, exists := index[key]; exists {
			out[pos] = mergeToolCall(out[pos], call)
			continue
		}
		index[key] = len(out)
		out = append(out, call)
	}
	return out
}

func callKey(call model.ToolCall) string {
	callID := strings.TrimSpace(call.ID)
	if callID != "" {
		return callID + "|" + call.Name
	}
	if strings.TrimSpace(call.Args) == "" {
		return call.Name
	}
	return call.Name + "|" + strings.TrimSpace(call.Args)
}

func mergeToolCall(oldCall model.ToolCall, newCall model.ToolCall) model.ToolCall {
	merged := oldCall
	if strings.TrimSpace(merged.ID) == "" {
		merged.ID = newCall.ID
	}
	if strings.TrimSpace(newCall.Name) != "" {
		merged.Name = newCall.Name
	}
	if merged.ThoughtSignature == "" && newCall.ThoughtSignature != "" {
		merged.ThoughtSignature = newCall.ThoughtSignature
	}
	if strings.TrimSpace(newCall.Args) != "" {
		merged.Args = newCall.Args
	}
	return merged
}
