package chat

import (
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// Rebuild only between complete model/tool batches. A callback waiting for
// approval retains its old binding; the MCP manager independently gates it.
func (a *Agent) refreshToolVisibility(ctx agent.Context, visibility *tool.ToolVisibility) {
	*visibility = tool.NewToolVisibilityForModel(a.tools, a.model)
	a.refreshDeferredTools(visibility)
	if !visibility.RetirementEnabled() {
		for event := range ctx.Events().All() {
			if event == nil {
				continue
			}
			visibility.ApplyDiscoveredToolNames(tool.DiscoveredToolNamesFromMetadata(event.Meta))
			if event.Tool != nil {
				visibility.ApplyToolResult(event.Tool.Name, event.Tool.Output)
			}
		}
		visibility.ApplyDiscoveredToolNames(a.discoveredTools)
		return
	}
	state := a.exposure
	if state == nil {
		initial := tool.ExposureState{Version: 1}
		for event := range ctx.Events().All() {
			if event != nil {
				if value, ok := tool.ExposureFromMetadata(event.Meta); ok {
					initial = value
				}
			}
		}
		state = &initial
	}
	visibility.RestoreExposure(*state)
	// Keep unresolved bindings while an asynchronous source is initializing.
	// They remain non-callable unless the exact version becomes ready.
}

func (a *Agent) recordExposure(visibility *tool.ToolVisibility, events []*session.Event) {
	if !visibility.RetirementEnabled() {
		return
	}
	for _, event := range events {
		if event == nil || event.Tool == nil {
			continue
		}
		if event.Tool.Name != tool.RetireToolName && event.Tool.Name != tool.ToolSearchToolName {
			visibility.KeepTool(event.Tool.Name)
		}
	}
	state := visibility.Exposure()
	a.exposure = &state
	// The batch has drained; persist one final projection, in canonical result
	// order. Concurrent workers never write this state or overwrite each other.
	if len(events) > 0 {
		event := events[len(events)-1]
		if event != nil {
			if event.Meta == nil {
				event.Meta = map[string]any{}
			}
			event.Meta[tool.MetadataToolExposure] = state
		}
	}
}

func admitRetirementResult(call model.ToolCall, result tool.Result, visibility *tool.ToolVisibility) tool.Result {
	if call.Name != tool.RetireToolName || result.IsError || !visibility.RetirementEnabled() {
		return result
	}
	if err := visibility.AdmitRetirement(toolResultRawOutput(result)); err != nil {
		return modelVisibleToolErrorResult(call, result, err)
	}
	return result
}
