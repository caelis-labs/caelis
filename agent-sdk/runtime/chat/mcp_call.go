package chat

import (
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type boundMCPCall struct {
	original model.ToolCall
	call     model.ToolCall
	target   tool.Tool
	binding  tool.Binding
	err      error
}

func (a *Agent) mcpTools() map[string]tool.Tool {
	out := map[string]tool.Tool{}
	candidates := append([]tool.Tool(nil), a.tools...)
	if a.deferredTools != nil {
		candidates = append(candidates, a.deferredTools.Tools()...)
	}
	for _, one := range candidates {
		if one != nil && tool.IsMCPDefinition(one.Definition()) && tool.AvailableForModel(one, a.model) {
			out[one.Definition().Name] = one
		}
	}
	return out
}

// Bind once per complete model response, before approval or any sibling tool
// executes. A later discovery or refresh cannot redirect a pending invocation.
func (a *Agent) bindMCPCalls(calls []model.ToolCall, visibility *tool.ToolVisibility) {
	a.boundCalls = map[string]boundMCPCall{}
	current := a.mcpTools()
	for _, call := range calls {
		declaration, ok := a.lookupRunTool(call.Name, visibility)
		if !ok || !tool.IsMCPCallDefinition(declaration.Definition()) {
			continue
		}
		bound := boundMCPCall{original: call}
		actual, err := tool.ParseMCPCall(call)
		bound.call = actual
		bound.err = err
		if err == nil {
			binding, known := a.discoveries[actual.Name]
			bound.binding = binding
			one := current[actual.Name]
			switch {
			case !known:
				bound.err = tool.NewError(tool.ErrorCodeNotFound, "MCP tool has not been discovered in this Session; use tool_search")
			case one == nil || binding != tool.BindingForDefinition(one.Definition()):
				bound.err = tool.NewError(tool.ErrorCodeStaleRevision, "MCP binding changed or is unavailable; use tool_search after refresh")
			default:
				bound.target = one
				bound.err = tool.ValidateMCPArguments(one.Definition(), []byte(actual.Args))
			}
		}
		a.boundCalls[call.ID] = bound
	}
}

func (a *Agent) presentMCPEvent(event *session.Event) {
	if event == nil || event.Tool == nil {
		return
	}
	bound, ok := a.boundCalls[event.Tool.ID]
	if !ok || bound.call.Name == "" {
		return
	}
	original := bound.original
	event.Tool.ModelCall = &original
	event.Tool.Name = bound.call.Name
	event.Tool.Input = mustObject(bound.call.Args)
	event.Meta = mergeEventMeta(event.Meta, toolMeta(bound.call.Name))
}

func (a *Agent) boundTool(call model.ToolCall, visibility *tool.ToolVisibility) (tool.Tool, model.ToolCall, *tool.Binding, error) {
	if bound, ok := a.boundCalls[call.ID]; ok {
		if bound.err != nil {
			return nil, bound.call, nil, bound.err
		}
		return bound.target, bound.call, &bound.binding, nil
	}
	one, ok := a.lookupRunTool(call.Name, visibility)
	if !ok {
		return nil, call, nil, tool.NewError(tool.ErrorCodeNotFound, fmt.Sprintf("tool %q not found", call.Name))
	}
	if tool.IsMCPCallDefinition(one.Definition()) {
		return nil, call, nil, tool.NewError(tool.ErrorCodeInvalidInput, "MCP call is not bound to a model response")
	}
	return one, call, nil, nil
}
