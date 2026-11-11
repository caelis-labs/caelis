package chat

import (
	"context"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/mcpdiscovery"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func (a *Agent) admitToolSearchResult(ctx context.Context, def tool.Definition, call model.ToolCall, result tool.Result) tool.Result {
	if result.IsError || !tool.IsToolSearchDefinition(def) || call.Name != tool.ToolSearchToolName {
		return result
	}
	selected := tool.ParseToolSearchOutput(toolResultRawOutput(result))
	names := make([]string, 0, len(selected.Tools))
	for _, entry := range selected.Tools {
		names = append(names, entry.Name)
	}
	admitted, err := mcpdiscovery.Admit(selected, a.mcpTools(ctx, names), result)
	if err != nil {
		return modelVisibleToolErrorResult(call, result, err)
	}
	return admitted
}
