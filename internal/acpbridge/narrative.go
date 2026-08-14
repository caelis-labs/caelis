package acpbridge

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	acpschema "github.com/caelis-labs/caelis/protocol/acp/schema"
)

type narrativeAccumulator struct {
	final              acpschema.FinalAssistantAccumulator
	reasoning          acpschema.FinalAssistantAccumulator
	lastNarrativeEvent *session.Event
	lastAssistantEvent *session.Event
}

func (a *narrativeAccumulator) normalize(event *session.Event) (*session.Event, *session.Event, bool) {
	if a == nil || !isControllerNarrativeChunk(event) {
		return event, nil, false
	}
	updateType := eventUpdateType(event)
	raw := narrativeEventText(event, updateType)
	a.lastNarrativeEvent = session.CloneEvent(event)
	if updateType == string(session.ProtocolUpdateTypeAgentMessage) {
		a.reasoning.Reset()
		a.lastAssistantEvent = session.CloneEvent(event)
		update := a.final.ObserveFrame(narrativeMessageID(event), raw)
		if update.Text == "" && update.Delta == "" {
			return nil, nil, true
		}
		if update.Delta == "" {
			return nil, nil, true
		}
		live := session.CloneEvent(event)
		live.ID = ""
		live.Visibility = session.VisibilityUIOnly
		setNarrativeEventText(live, updateType, update.Delta)
		return nil, live, true
	}
	a.final.Reset()
	update := a.reasoning.ObserveFrame(narrativeMessageID(event), raw)
	if update.Text == "" && update.Delta == "" {
		return nil, nil, true
	}
	if update.Delta == "" {
		return nil, nil, true
	}
	live := session.CloneEvent(event)
	live.ID = ""
	live.Visibility = session.VisibilityUIOnly
	setNarrativeEventText(live, updateType, update.Delta)
	return nil, live, true
}

func (a *narrativeAccumulator) observeBarrier(event *session.Event) {
	if a == nil || event == nil || event.Scope == nil || event.Protocol == nil {
		return
	}
	switch eventUpdateType(event) {
	case string(session.ProtocolUpdateTypeToolCall), string(session.ProtocolUpdateTypeToolUpdate), string(session.ProtocolUpdateTypePlan):
		a.final.Reset()
		a.reasoning.Reset()
	}
}

func narrativeMessageID(event *session.Event) string {
	if event == nil || event.Protocol == nil || event.Protocol.Update == nil {
		return ""
	}
	return strings.TrimSpace(event.Protocol.Update.MessageID)
}

func (a *narrativeAccumulator) finalAssistantEvent() *session.Event {
	if a == nil || strings.TrimSpace(a.final.FinalText()) == "" || a.lastAssistantEvent == nil {
		return nil
	}
	event := session.CloneEvent(a.lastAssistantEvent)
	event.ID = ""
	event.Visibility = session.VisibilityCanonical
	event.Type = session.EventTypeAssistant
	setNarrativeEventText(event, string(session.ProtocolUpdateTypeAgentMessage), a.final.FinalText())
	return event
}

func isControllerNarrativeChunk(event *session.Event) bool {
	if event == nil || event.Protocol == nil || event.Scope == nil {
		return false
	}
	switch eventUpdateType(event) {
	case string(session.ProtocolUpdateTypeAgentMessage), string(session.ProtocolUpdateTypeAgentThought):
		return true
	default:
		return false
	}
}

func eventUpdateType(event *session.Event) string {
	return session.ProtocolSessionUpdateType(event)
}

func shouldPersistExternalACPEvent(event *session.Event) bool {
	if event == nil || !session.IsCanonicalHistoryEvent(event) || session.IsUIOnly(event) {
		return false
	}
	switch session.EventTypeOf(event) {
	case session.EventTypeUser, session.EventTypeAssistant:
		return true
	default:
		return false
	}
}

func narrativeEventText(event *session.Event, updateType string) string {
	if event == nil {
		return ""
	}
	if event.Message != nil {
		switch strings.TrimSpace(updateType) {
		case string(session.ProtocolUpdateTypeAgentThought):
			if text := event.Message.ReasoningText(); text != "" {
				return text
			}
		default:
			if text := event.Message.TextContent(); text != "" {
				return text
			}
		}
	}
	return session.EventText(event)
}

func setNarrativeEventText(event *session.Event, updateType string, text string) {
	if event == nil {
		return
	}
	event.Text = text
	if event.Protocol != nil {
		protocol := session.CloneEventProtocol(*event.Protocol)
		if protocol.Update == nil {
			protocol.Update = &session.ProtocolUpdate{}
		}
		protocol.Update.SessionUpdate = strings.TrimSpace(updateType)
		protocol.Update.Content = session.ProtocolTextContent(text)
		event.Protocol = &protocol
	}
	switch strings.TrimSpace(updateType) {
	case string(session.ProtocolUpdateTypeAgentThought):
		message := model.NewReasoningMessage(model.RoleAssistant, text, model.ReasoningVisibilityVisible)
		event.Message = &message
	default:
		message := model.NewTextMessage(model.RoleAssistant, text)
		event.Message = &message
	}
}

func appendNarrativeText(existing string, incoming string) (string, string) {
	return acpschema.AppendAssistantText(existing, incoming)
}
