package chat

import (
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/mcpdiscovery"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func (a *Agent) admitToolSearchResult(def tool.Definition, call model.ToolCall, result tool.Result) tool.Result {
	if result.IsError || !tool.IsToolSearchDefinition(def) || call.Name != tool.ToolSearchToolName {
		return result
	}
	admitted, err := mcpdiscovery.Admit(tool.ParseToolSearchOutput(toolResultRawOutput(result)), a.mcpTools(), result)
	if err != nil {
		return modelVisibleToolErrorResult(call, result, err)
	}
	return admitted
}
