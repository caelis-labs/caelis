package local

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

type acpNarrativeAccumulator struct {
	assistantText      string
	reasoningText      string
	lastNarrativeEvent *session.Event
	lastAssistantEvent *session.Event
}

func (a *acpNarrativeAccumulator) normalize(event *session.Event) (*session.Event, *session.Event, bool) {
	if a == nil || !isACPControllerNarrativeChunk(event) {
		return event, nil, false
	}
	updateType := strings.TrimSpace(event.Protocol.UpdateType)
	raw := narrativeEventText(event, updateType)
	a.lastNarrativeEvent = session.CloneEvent(event)
	if updateType == string(session.ProtocolUpdateTypeAgentMessage) {
		a.lastAssistantEvent = session.CloneEvent(event)
	}
	cumulative, delta := a.append(updateType, raw)
	if cumulative == "" && delta == "" {
		return nil, nil, true
	}
	if delta == "" {
		return nil, nil, true
	}
	live := session.CloneEvent(event)
	live.ID = ""
	live.Visibility = session.VisibilityUIOnly
	setNarrativeEventText(live, updateType, delta)
	return nil, live, true
}

func (a *acpNarrativeAccumulator) append(updateType string, text string) (string, string) {
	switch strings.TrimSpace(updateType) {
	case string(session.ProtocolUpdateTypeAgentThought):
		cumulative, delta := appendNarrativeText(a.reasoningText, text)
		a.reasoningText = cumulative
		return cumulative, delta
	default:
		cumulative, delta := appendNarrativeText(a.assistantText, text)
		a.assistantText = cumulative
		return cumulative, delta
	}
}

func (a *acpNarrativeAccumulator) finalAssistantEvent() *session.Event {
	if a == nil || strings.TrimSpace(a.assistantText) == "" || a.lastAssistantEvent == nil {
		return nil
	}
	event := session.CloneEvent(a.lastAssistantEvent)
	event.ID = ""
	event.Visibility = session.VisibilityCanonical
	event.Type = session.EventTypeAssistant
	setNarrativeEventText(event, string(session.ProtocolUpdateTypeAgentMessage), a.assistantText)
	return event
}

func isACPControllerNarrativeChunk(event *session.Event) bool {
	if event == nil || event.Protocol == nil || event.Scope == nil {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(event.Scope.Source)), "acp") {
		return false
	}
	switch strings.TrimSpace(event.Protocol.UpdateType) {
	case string(session.ProtocolUpdateTypeAgentMessage), string(session.ProtocolUpdateTypeAgentThought):
		return true
	default:
		return false
	}
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

func isACPControllerUserEcho(event *session.Event) bool {
	if event == nil || event.Scope == nil {
		return false
	}
	if session.EventTypeOf(event) != session.EventTypeUser {
		return false
	}
	if event.Scope.Participant.ID != "" {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(event.Scope.Source)), "acp")
}

func isACPParticipantUserEcho(event *session.Event) bool {
	if event == nil || event.Scope == nil {
		return false
	}
	if session.EventTypeOf(event) != session.EventTypeUser {
		return false
	}
	if strings.TrimSpace(event.Scope.Participant.ID) == "" {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(event.Scope.Source)), "acp")
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
	if incoming == "" {
		return existing, ""
	}
	if existing == "" {
		return incoming, incoming
	}
	if strings.HasPrefix(incoming, existing) {
		delta := incoming[len(existing):]
		return incoming, delta
	}
	if strings.HasPrefix(existing, incoming) {
		return existing, ""
	}
	return existing + incoming, incoming
}
