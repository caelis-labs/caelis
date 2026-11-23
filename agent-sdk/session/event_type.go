package session

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// EventType identifies one canonical session event kind.
type EventType string

const (
	EventTypeUser EventType = "user"
	// EventTypeInteraction identifies command echoes in older Session histories.
	// Control no longer writes these records. Keep the type while that event
	// schema is supported by replay.
	EventTypeInteraction EventType = "interaction"
	EventTypeAssistant   EventType = "assistant"
	EventTypePlan        EventType = "plan"
	EventTypeToolCall    EventType = "tool_call"
	EventTypeToolResult  EventType = "tool_result"
	EventTypeParticipant EventType = "participant"
	EventTypeHandoff     EventType = "handoff"
	EventTypeCompact     EventType = "compact"
	EventTypeNotice      EventType = "notice"
	EventTypeLifecycle   EventType = "lifecycle"
	EventTypeSystem      EventType = "system"
	EventTypeContext     EventType = "context"
	EventTypeCustom      EventType = "custom"
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
		protocol := event.Protocol
		if update := protocol.Update; update != nil {
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
		switch strings.TrimSpace(protocol.Method) {
		case "":
			if protocol.AgentCommunication != nil {
				return EventTypeContext
			}
		case ProtocolMethodParticipantUpdate:
			return EventTypeParticipant
		case ProtocolMethodControllerHandoff:
			return EventTypeHandoff
		case ProtocolMethodRuntimeLifecycle, ProtocolMethodRequestPermission:
			return EventTypeLifecycle
		case ProtocolMethodContextCheckpoint:
			return EventTypeCompact
		case ProtocolMethodAgentCommunication:
			return EventTypeContext
		}
		if protocol.Permission != nil {
			return EventTypeLifecycle
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
