package mcpdiscovery

import (
	"encoding/json"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// Admit projects only exact current bindings, under the discovery result budget.
// Both canonical model calls and nested describe use this single admission owner.
func Admit(selected tool.ToolSearchResult, current map[string]tool.Tool, result tool.Result) (tool.Result, error) {
	admitted := tool.ToolSearchResult{Truncated: selected.Truncated, OmittedCount: selected.OmittedCount}
	for _, entry := range selected.Tools {
		one := current[entry.Name]
		if one == nil || entry.Binding == nil || entry.Binding.Identity == "" || entry.Binding.Version == "" {
			admitted.OmittedCount++
			admitted.Truncated = true
			continue
		}
		def := one.Definition()
		if *entry.Binding != tool.BindingForDefinition(def) {
			admitted.OmittedCount++
			admitted.Truncated = true
			continue
		}
		full := tool.NewToolSearchDiscoveredTool(def)
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
		return result, err
	}
	result.Content = []model.Part{model.NewJSONPart(raw)}
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata[MetadataKey] = true
	return result, nil
}
