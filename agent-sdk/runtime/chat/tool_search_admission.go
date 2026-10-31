package chat

import (
	"encoding/json"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/mcpdiscovery"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func (a *Agent) admitToolSearchResult(def tool.Definition, call model.ToolCall, result tool.Result) tool.Result {
	if result.IsError || !tool.IsToolSearchDefinition(def) || call.Name != tool.ToolSearchToolName {
		return result
	}
	selected := tool.ParseToolSearchOutput(toolResultRawOutput(result))
	admitted := tool.ToolSearchResult{Truncated: selected.Truncated, OmittedCount: selected.OmittedCount}
	current := a.mcpTools()
	for _, entry := range selected.Tools {
		one := current[entry.Name]
		if one == nil || entry.Binding == nil || entry.Binding.Identity == "" || entry.Binding.Version == "" || *entry.Binding != tool.BindingForDefinition(one.Definition()) {
			admitted.OmittedCount++
			admitted.Truncated = true
			continue
		}
		full := tool.NewToolSearchDiscoveredTool(one.Definition())
		trial := admitted
		trial.Tools = append(append([]tool.ToolSearchDiscoveredTool(nil), admitted.Tools...), full)
		trial.Count = len(trial.Tools)
		if tool.EstimateToolSearchResultPromptTokens(trial) > tool.MaxToolSearchResultPromptTokens {
			admitted.OmittedCount++
			admitted.Truncated = true
			continue
		}
		admitted.Tools = trial.Tools
	}
	admitted.Count = len(admitted.Tools)
	raw, err := json.Marshal(admitted)
	if err != nil {
		return modelVisibleToolErrorResult(call, result, err)
	}
	result.Content = []model.Part{model.NewJSONPart(raw)}
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata[mcpdiscovery.MetadataKey] = true
	return result
}
