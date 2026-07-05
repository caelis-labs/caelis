package runtime

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func promptEventsWithToolVisibilityMetadata(promptEvents []*session.Event, sourceEvents []*session.Event) []*session.Event {
	names := discoveredToolNamesFromEvents(sourceEvents)
	if len(names) == 0 || len(promptEvents) == 0 {
		return promptEvents
	}
	out := session.CloneEvents(promptEvents)
	for _, event := range out {
		if event == nil {
			continue
		}
		if event.Meta == nil {
			event.Meta = map[string]any{}
		}
		event.Meta[tool.MetadataDiscoveredToolNames] = tool.DiscoveredToolNamesMetadataValue(names)
		return out
	}
	return out
}

func discoveredToolNamesFromEvents(events []*session.Event) []string {
	if len(events) == 0 {
		return nil
	}
	names := make([]string, 0)
	for _, event := range events {
		if event == nil {
			continue
		}
		names = append(names, tool.DiscoveredToolNamesFromMetadata(event.Meta)...)
		if data, ok := compact.CompactEventDataFromEvent(event); ok {
			names = append(names, data.DiscoveredTools...)
		}
		if event.Tool == nil || !strings.EqualFold(strings.TrimSpace(event.Tool.Name), tool.ToolSearchToolName) {
			continue
		}
		names = append(names, tool.ParseToolSearchOutput(event.Tool.Output).DiscoveredToolNames()...)
	}
	return tool.DiscoveredToolNamesMetadataValue(names)
}
