package acpbridge

import (
	"slices"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type narrativeAccumulator struct {
	final              FinalAssistantAccumulator
	reasoning          FinalAssistantAccumulator
	lastNarrativeEvent *session.Event
	lastAssistantEvent *session.Event
	userObservations   map[string]struct{}
}

func (a *narrativeAccumulator) normalize(event *session.Event) (*session.Event, *session.Event, bool) {
	if a == nil || !isControllerNarrativeChunk(event) {
		return event, nil, false
	}
	updateType := eventUpdateType(event)
	raw := narrativeEventText(event, updateType)
	replacement := event.Protocol.Update != nil && event.Protocol.Update.Replacement
	metaPatch := event.Protocol.Update != nil && slices.Contains(event.Protocol.Update.ReplacedFields, "_meta")
	contentChanged := raw != "" || replacement
	if updateType == string(session.ProtocolUpdateTypeAgentMessage) {
		update := a.final.observeFrame(narrativeMessageID(event), raw, replacement, event.Protocol.Update.PreviouslyObserved)
		if contentChanged && update.MessageID == a.final.messageID {
			a.reasoning.AdvanceBoundary()
		}
		identified := withNarrativeMessageID(event, update.MessageID)
		a.lastNarrativeEvent = session.CloneEvent(identified)
		if update.MessageID == a.final.messageID {
			a.lastAssistantEvent = session.CloneEvent(identified)
		}
		if update.Text == "" && update.Delta == "" && !replacement && !metaPatch {
			return nil, nil, true
		}
		if update.Delta == "" && !replacement && !metaPatch {
			return nil, nil, true
		}
		live := session.CloneEvent(identified)
		live.ID = ""
		live.Visibility = session.VisibilityUIOnly
		setNarrativeEventText(live, updateType, update.Delta)
		return nil, live, true
	}
	a.final.observeContentChunk(updateType, raw, narrativeMessageID(event), replacement, event.Protocol.Update.PreviouslyObserved)
	update := a.reasoning.observeFrame(narrativeMessageID(event), raw, replacement, event.Protocol.Update.PreviouslyObserved)
	identified := withNarrativeMessageID(event, update.MessageID)
	a.lastNarrativeEvent = session.CloneEvent(identified)
	if update.Text == "" && update.Delta == "" && !replacement && !metaPatch {
		return nil, nil, true
	}
	if update.Delta == "" && !replacement && !metaPatch {
		return nil, nil, true
	}
	live := session.CloneEvent(identified)
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
	case string(session.ProtocolUpdateTypeToolCall), string(session.ProtocolUpdateTypeToolUpdate):
		update := session.ProtocolUpdateOf(event)
		id, previous := "", false
		if update != nil {
			id, previous = update.ToolCallID, update.PreviouslyObserved
		}
		if a.final.observeToolCall(id, previous).Barrier {
			a.reasoning.AdvanceBoundary()
		}
	case string(session.ProtocolUpdateTypePlan):
		a.final.AdvanceBoundary()
		a.reasoning.AdvanceBoundary()
	}
}

func narrativeMessageID(event *session.Event) string {
	if event == nil || event.Protocol == nil || event.Protocol.Update == nil {
		return ""
	}
	return event.Protocol.Update.MessageID
}

func withNarrativeMessageID(event *session.Event, messageID string) *session.Event {
	identified := session.CloneEvent(event)
	if identified == nil || messageID == "" {
		return identified
	}
	if identified.Protocol == nil {
		identified.Protocol = &session.EventProtocol{}
	}
	protocol := session.CloneEventProtocol(*identified.Protocol)
	if protocol.Update == nil {
		protocol.Update = &session.ProtocolUpdate{}
	}
	protocol.Update.MessageID = messageID
	identified.Protocol = &protocol
	return identified
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
	if event == nil || session.IsUIOnly(event) {
		return false
	}
	if event.ContextUsage != nil && session.IsMirror(event) {
		return true
	}
	if session.IsMirror(event) && eventUpdateType(event) == string(session.ProtocolUpdateTypeUserMessage) {
		return true
	}
	if !session.IsCanonicalHistoryEvent(event) {
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
		if text == "" && (protocol.Update.Replacement || slices.Contains(protocol.Update.ReplacedFields, "_meta")) {
			protocol.Update.Content = map[string]any{"type": "text", "text": ""}
		}
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

// userObservation follows an edited provider message's display frames. It never
// turns those observations into a new native input or an execution receipt.
func (a *narrativeAccumulator) userObservation(update *session.ProtocolUpdate) bool {
	if a == nil || update == nil {
		return false
	}
	id := update.MessageID
	if update.Replacement || slices.Contains(update.ReplacedFields, "_meta") {
		if id != "" {
			if a.userObservations == nil {
				a.userObservations = map[string]struct{}{}
			}
			a.userObservations[id] = struct{}{}
		}
		return true
	}
	_, found := a.userObservations[id]
	return id != "" && found
}
