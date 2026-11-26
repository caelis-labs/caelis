package runtime

import (
	"encoding/json"
	"errors"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

// applyMCPDiscoveryBindings restores bindings from a successful, Runtime-observed exact search.
func applyMCPDiscoveryBindings(out map[string]tool.Binding, event *session.Event) {
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

// withMCPDiscoveryEntryHint adds recovery guidance at the invoking entry point.
// Manager errors describe catalog state without assuming the caller's model interface.
func withMCPDiscoveryEntryHint(err error, entry string) error {
	if !errorcode.Is(err, tool.ErrorCodeStaleRevision) {
		return err
	}
	hint := "Refresh the catalog if needed, then use " + entry + " to rediscover before calling."
	var existing *tool.ToolError
	if errors.As(err, &existing) && existing.Hint == hint {
		return err
	}
	out := tool.WrapError(tool.ErrorCodeStaleRevision, err, err.Error()+"; "+hint)
	out.Hint = hint
	return out
}
