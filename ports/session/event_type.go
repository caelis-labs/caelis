package session

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/model"
)

// EventTypeOf backfills an event type for deserialized or adapter-produced
// envelopes that predate explicit Event.Type. Runtime writers should set Type
// when producing new canonical events.
func EventTypeOf(event *Event) EventType {
	if event == nil {
		return EventTypeCustom
	}
	if event.Type != "" {
		return event.Type
	}
	switch {
	case event.PlanPayload != nil:
		return EventTypePlan
	}
	if IsNotice(event) {
		return EventTypeNotice
	}
	if event.Lifecycle != nil {
		return EventTypeLifecycle
	}
	if event.Protocol != nil {
		switch strings.TrimSpace(event.Protocol.Method) {
		case ProtocolMethodParticipantUpdate:
			return EventTypeParticipant
		case ProtocolMethodControllerHandoff:
			return EventTypeHandoff
		case ProtocolMethodRuntimeLifecycle, ProtocolMethodRequestPermission:
			return EventTypeLifecycle
		case ProtocolMethodContextCheckpoint:
			return EventTypeCompact
		}
		if update := ProtocolUpdateOf(event); update != nil {
			switch strings.TrimSpace(update.SessionUpdate) {
			case string(ProtocolUpdateTypeUserMessage):
				return EventTypeUser
			case string(ProtocolUpdateTypeAgentMessage), string(ProtocolUpdateTypeAgentThought):
				return EventTypeAssistant
			case string(ProtocolUpdateTypeToolCall):
				return EventTypeToolCall
			case string(ProtocolUpdateTypeToolUpdate):
				return EventTypeToolResult
			case string(ProtocolUpdateTypePlan):
				return EventTypePlan
			}
		}
		switch {
		case event.Protocol.Plan != nil || strings.EqualFold(strings.TrimSpace(event.Protocol.UpdateType), "plan"):
			return EventTypePlan
		case event.Protocol.ToolCall != nil:
			return EventTypeToolCall
		case event.Protocol.Approval != nil:
			return EventTypeLifecycle
		case event.Protocol.Participant != nil:
			return EventTypeParticipant
		case event.Protocol.Handoff != nil:
			return EventTypeHandoff
		}
	}
	if event.Tool != nil {
		switch strings.ToLower(strings.TrimSpace(event.Tool.Status)) {
		case "completed", "failed", "error", "interrupted", "cancelled", "canceled", "terminated":
			return EventTypeToolResult
		default:
			return EventTypeToolCall
		}
	}
	if event.Message == nil {
		return EventTypeCustom
	}
	switch event.Message.Role {
	case model.RoleUser:
		return EventTypeUser
	case model.RoleAssistant:
		if len(event.Message.ToolCalls()) > 0 {
			return EventTypeToolCall
		}
		return EventTypeAssistant
	case model.RoleTool:
		return EventTypeToolResult
	case model.RoleSystem:
		return EventTypeSystem
	default:
		return EventTypeCustom
	}
}
