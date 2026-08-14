package chat

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// admitToolSearchResult rewrites discovery output before the Runtime constructs
// a model message or durable event. The rewritten payload contains only
// canonical registered definitions admitted by the run-local visibility
// budget, so live execution and replay observe the same bounded result.
func admitToolSearchResult(def tool.Definition, call model.ToolCall, result tool.Result, visibility *tool.ToolVisibility) tool.Result {
	if visibility == nil || result.IsError ||
		!tool.IsToolSearchDefinition(def) ||
		!strings.EqualFold(strings.TrimSpace(call.Name), tool.ToolSearchToolName) {
		return result
	}
	admitted := visibility.AdmitToolSearchResult(tool.ParseToolSearchOutput(toolResultRawOutput(result)))
	raw, err := json.Marshal(admitted)
	if err != nil {
		return result
	}
	result.Content = []model.Part{model.NewJSONPart(raw)}
	return result
}
