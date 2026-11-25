package mcpdiscovery

import (
	"encoding/json"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

// Apply restores bindings from a successful, Runtime-observed exact search.
func Apply(out map[string]tool.Binding, event *session.Event) {
	if event == nil || event.Type != session.EventTypeToolResult || event.Tool == nil {
		return
	}
	fact := event.Tool
	if fact.Name != mcp.SearchToolName || !fact.Discovery || fact.Outcome != session.ToolOutcomeSucceeded || fact.Result == nil {
		return
	}
	nested := fact.Parent != nil && fact.Parent.Kind == "code_mode" && event.Scope != nil && event.Scope.Source == "runtime_tool"
	if !session.IsMainInvocationVisibleEvent(event) && !nested {
		return
	}
	value, ok := fact.Result.Structured()
	if !ok {
		return
	}
	var raw map[string]any
	if json.Unmarshal(value, &raw) != nil {
		return
	}
	selected := mcp.ParseToolSearchOutput(raw)
	for _, entry := range selected.Tools {
		b := entry.Binding
		if b != nil && entry.Name == b.Name && entry.InputSchema != nil && b.Name != "" && b.Identity != "" && b.Version != "" {
			out[b.Name] = *b
		}
	}
}
