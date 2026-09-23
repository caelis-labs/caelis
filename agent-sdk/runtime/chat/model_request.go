package chat

import (
	"slices"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func (a *Agent) rememberRequestToolDiscovery(event *session.Event) {
	if a.resolveModelRequest == nil {
		return
	}
	names := tool.DiscoveredToolNamesFromMetadata(event.Meta)
	if event.Tool.Name == tool.ToolSearchToolName {
		names = append(names, tool.ParseToolSearchOutput(event.Tool.Output).DiscoveredToolNames()...)
	}
	for _, name := range names {
		if len(a.discoveredTools) < tool.MaxDeferredToolsPerRun && !slices.Contains(a.discoveredTools, name) {
			a.discoveredTools = append(a.discoveredTools, name)
		}
	}
}

func (a *Agent) refreshModelRequest(ctx agent.Context, visibility *tool.ToolVisibility) error {
	if a.resolveModelRequest == nil {
		return nil
	}
	snapshot, err := a.resolveModelRequest(ctx)
	if err != nil {
		return err
	}
	snapshot = agent.CloneModelRequestSnapshot(snapshot)
	next, err := NewWithTools(a.name, snapshot.Model, snapshot.Tools, "")
	if err != nil {
		return err
	}
	next.deferredTools = snapshot.DeferredTools
	next.instructions, next.reasoning, next.request = snapshot.Instructions, snapshot.Reasoning, snapshot.Request
	next.admitModelRequest = snapshot.Admit
	next.resolveModelRequest = a.resolveModelRequest
	next.toolResultArtifacts = a.toolResultArtifacts
	next.discoveredTools = a.discoveredTools
	*a = *next
	*visibility = tool.NewToolVisibilityForModel(a.tools, a.model)
	a.refreshDeferredTools(visibility)
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
	return nil
}
