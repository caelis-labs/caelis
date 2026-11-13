package mcpdiscovery

import (
	"encoding/json"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// Admit projects only exact current bindings, under the discovery result budget.
// Both canonical model calls and nested search use this single admission owner.
// Service pages must retain every selected entry so their continuation cannot
// skip tools or report successful completion after a scope or binding change.
func Admit(selected tool.ToolSearchResult, current map[string]tool.Tool, result tool.Result, input json.RawMessage) (tool.Result, error) {
	var request struct {
		Server string `json:"server"`
	}
	service := json.Unmarshal(input, &request) == nil && request.Server != ""
	admitted := tool.ToolSearchResult{Truncated: selected.Truncated, OmittedCount: selected.OmittedCount, Next: selected.Next}
	for _, entry := range selected.Tools {
		one := current[entry.Name]
		if one == nil || entry.Binding == nil || entry.Binding.Identity == "" || entry.Binding.Version == "" {
			if service {
				return result, tool.NewError(tool.ErrorCodeStaleRevision, "service directory changed; restart discovery")
			}
			admitted.OmittedCount++
			admitted.Truncated = true
			continue
		}
		def := one.Definition()
		if *entry.Binding != tool.BindingForDefinition(def) {
			if service {
				return result, tool.NewError(tool.ErrorCodeStaleRevision, "service directory changed; restart discovery")
			}
			admitted.OmittedCount++
			admitted.Truncated = true
			continue
		}
		full := tool.NewToolSearchDiscoveredTool(def)
		trial := admitted
		trial.Tools = append(append([]tool.ToolSearchDiscoveredTool(nil), admitted.Tools...), full)
		trial.Count = len(trial.Tools)
		if tool.EstimateToolSearchResultPromptTokens(trial) > tool.MaxToolSearchResultPromptTokens {
			if service {
				return result, tool.NewError(errorcode.ResourceExhausted, "complete tool schemas exceed the service search budget")
			}
			admitted.OmittedCount++
			admitted.Truncated = true
			continue
		}
		admitted.Tools = trial.Tools
	}
	admitted.Count = len(admitted.Tools)
	if admitted.Next != "" {
		if len(admitted.Tools) == 0 {
			admitted.Next = ""
		} else {
			admitted.Next = admitted.Tools[len(admitted.Tools)-1].Name
		}
	}
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
