package runtime

import (
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func promptEventsWithToolVisibilityMetadata(promptEvents []*session.Event, sourceEvents []*session.Event, retirement bool) []*session.Event {
	if len(promptEvents) == 0 {
		return promptEvents
	}
	out := session.CloneEvents(promptEvents)
	for _, event := range out {
		if event != nil {
			delete(event.Meta, tool.MetadataToolExposure)
			delete(event.Meta, tool.MetadataDiscoveredToolNames)
		}
	}
	state, hasExposure := exposureFromEvents(sourceEvents)
	names := discoveredToolNamesFromEvents(sourceEvents)
	for _, event := range out {
		if event == nil {
			continue
		}
		if event.Meta == nil {
			event.Meta = map[string]any{}
		}
		if retirement && hasExposure {
			event.Meta[tool.MetadataToolExposure] = state
		} else if !retirement {
			event.Meta[tool.MetadataDiscoveredToolNames] = tool.DiscoveredToolNamesMetadataValue(names)
		}
		break
	}
	return out
}

// Checkpoints replace prior discovery; later canonical discovery extends that
// baseline. An old retirement projection cannot override newer ordinary results.
func discoveredToolNamesFromEvents(events []*session.Event) []string {
	var names []string
	for _, event := range events {
		if event == nil {
			continue
		}
		if state, ok := exposureFromEvents([]*session.Event{event}); ok {
			names = nil
			for _, binding := range state.Active {
				names = append(names, binding.Name)
			}
		} else if data, ok := compact.CompactEventDataFromEvent(event); ok {
			names = append([]string(nil), data.DiscoveredTools...)
		}
		names = append(names, tool.DiscoveredToolNamesFromMetadata(event.Meta)...)
		if event.Tool != nil && event.Tool.Name == tool.ToolSearchToolName {
			names = append(names, tool.ParseToolSearchOutput(event.Tool.Output).DiscoveredToolNames()...)
		}
	}
	return tool.DiscoveredToolNamesMetadataValue(names)
}
