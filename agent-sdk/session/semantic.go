package session

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

type EventPlanPayload struct {
	Entries []EventPlanEntry `json:"entries,omitempty"`
}

type EventPlanEntry struct {
	Content  string `json:"content,omitempty"`
	Status   string `json:"status,omitempty"`
	Priority string `json:"priority,omitempty"`
}

// ModelMessageOf returns the canonical model-visible message carried by one
// event. It intentionally does not infer model replay input from Protocol,
// _meta, Text, or other projection-only state.
func ModelMessageOf(event *Event) (model.Message, bool) {
	if event == nil || event.Message == nil {
		return model.Message{}, false
	}
	return model.CloneMessage(*event.Message), true
}

func PlanPayloadOf(event *Event) *EventPlanPayload {
	if event == nil || event.PlanPayload == nil {
		return nil
	}
	out := cloneEventPlanPayload(*event.PlanPayload)
	return &out
}

func EventToolProjection(event *Event) *EventTool {
	if event == nil || event.Tool == nil {
		return nil
	}
	out := CloneEventTool(*event.Tool)
	return &out
}

// CanonicalToolName resolves the exact durable tool name from authoritative
// Event.Tool or Message tool-call data. Protocol title, kind, and display
// metadata are projection facts and never create a durable tool identity.
func CanonicalToolName(event *Event, update *ProtocolUpdate) string {
	if event == nil {
		return ""
	}
	if event.Tool != nil {
		if name := strings.TrimSpace(event.Tool.Name); name != "" {
			return name
		}
	}
	return messageToolCallName(event, update)
}

func messageToolCallName(event *Event, update *ProtocolUpdate) string {
	if event == nil || event.Message == nil {
		return ""
	}
	callID := ""
	if update != nil {
		callID = update.ToolCallID
	} else if event.Protocol != nil && event.Protocol.Update != nil {
		callID = event.Protocol.Update.ToolCallID
	}
	if callID == "" && event.Tool != nil {
		callID = event.Tool.ID
	}
	calls := event.Message.ToolCalls()
	if len(calls) == 0 {
		return ""
	}
	if callID != "" {
		for _, call := range calls {
			if call.ID != callID {
				continue
			}
			return strings.TrimSpace(call.Name)
		}
		return ""
	}
	if len(calls) == 1 {
		return strings.TrimSpace(calls[0].Name)
	}
	return ""
}

func cloneEventPlanPayload(in EventPlanPayload) EventPlanPayload {
	out := in
	if len(in.Entries) > 0 {
		out.Entries = make([]EventPlanEntry, 0, len(in.Entries))
		for _, item := range in.Entries {
			out.Entries = append(out.Entries, EventPlanEntry{
				Content:  strings.TrimSpace(item.Content),
				Status:   strings.TrimSpace(item.Status),
				Priority: strings.TrimSpace(item.Priority),
			})
		}
	}
	return out
}
