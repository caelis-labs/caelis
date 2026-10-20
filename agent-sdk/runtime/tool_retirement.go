package runtime

import (
	"slices"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func exposureFromEvents(events []*session.Event) (tool.ExposureState, bool) {
	var state tool.ExposureState
	found := false
	for _, event := range events {
		if event != nil {
			// A generated/ready checkpoint has no durable sequence. Only a
			// canonical compact event with identity and coverage can replace the
			// exposed set; rereading it is a pure, idempotent projection.
			if compact.IsCompactEvent(event) {
				data, ok := compact.CompactEventDataFromEvent(event)
				if !ok || event.ID == "" || event.Seq == 0 || data.ContractVersion != compact.CompactContractVersion ||
					data.SummarizedThroughID == "" || data.SummarizedThroughSeq == 0 || data.SummarizedThroughSeq >= event.Seq {
					continue
				}
			}
			if current, ok := tool.ExposureFromMetadata(event.Meta); ok {
				state = current
				found = true
			}
		}
	}
	return state, found
}

// Attach the next exposure to the same checkpoint append. No live state changes
// before a confirmed append, so failure, cancellation and CAS conflicts preserve
// the previous set. Async adopters must pass their revalidated current source,
// not the background generation snapshot, through this persistence boundary.
func (r *Runtime) checkpointExposure(event *session.Event, source []*session.Event, safe bool) {
	if !r.toolRetirement {
		return
	}
	state, ok := exposureFromEvents(source)
	if !ok {
		state = tool.ExposureState{Version: 1}
	}
	pending := map[string]bool{}
	for _, item := range source {
		if item == nil || item.Tool == nil {
			continue
		}
		turnID := ""
		if item.Scope != nil {
			turnID = item.Scope.TurnID
		}
		key := turnID + "/" + item.Tool.ID
		switch item.Type {
		case session.EventTypeToolCall:
			pending[key] = true
		case session.EventTypeToolResult:
			if item.Tool.Status != "in_progress" {
				delete(pending, key)
			}
		}
	}
	// Calls, approvals, cancellation/unknown results are owned by Runtime. Never
	// retire across an unresolved batch, even if a mark concerns another tool.
	if safe && len(pending) == 0 {
		state.Active = slices.DeleteFunc(state.Active, func(binding tool.Binding) bool { return slices.Contains(state.Pending, binding) })
		state.Pending = nil
	}
	if event.Meta == nil {
		event.Meta = map[string]any{}
	}
	event.Meta[tool.MetadataToolExposure] = state
	if data, ok := compact.CompactEventDataFromEvent(event); ok {
		data.DiscoveredTools = nil
		for _, binding := range state.Active {
			data.DiscoveredTools = append(data.DiscoveredTools, binding.Name)
		}
		event.Meta[compact.MetaKeyCompact] = compact.CompactEventDataValue(data)
	}
}
