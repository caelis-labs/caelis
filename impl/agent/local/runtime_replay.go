package local

import (
	"context"
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func (r *Runtime) persistInterruptedAssistantReplay(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	turnID string,
	events []*session.Event,
	cause error,
) error {
	if r == nil || r.sessions == nil {
		return nil
	}
	event := interruptedAssistantReplayEvent(activeSession, turnID, events, cause)
	if event == nil {
		return nil
	}
	_, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: ref,
		Event:      event,
	})
	return err
}

func interruptedAssistantReplayEvent(
	activeSession session.Session,
	turnID string,
	events []*session.Event,
	cause error,
) *session.Event {
	turnID = strings.TrimSpace(turnID)
	var answer strings.Builder
	var reasoning strings.Builder
	var template *session.Event
	for _, event := range events {
		if !mainTurnEvent(event, turnID) || session.EventTypeOf(event) != session.EventTypeAssistant {
			continue
		}
		if session.IsCanonicalHistoryEvent(event) && assistantEventHasReplayText(event) {
			return nil
		}
		if event.Visibility != session.VisibilityUIOnly {
			continue
		}
		updateType := assistantReplayUpdateType(event)
		text := narrativeEventText(event, updateType)
		if text == "" {
			continue
		}
		template = session.CloneEvent(event)
		switch updateType {
		case string(session.ProtocolUpdateTypeAgentThought):
			reasoning.WriteString(text)
		default:
			answer.WriteString(text)
		}
	}
	answerText := answer.String()
	reasoningText := reasoning.String()
	if strings.TrimSpace(answerText) == "" && strings.TrimSpace(reasoningText) == "" {
		return nil
	}
	if template == nil {
		return nil
	}
	return buildInterruptedAssistantReplayEvent(template, activeSession, turnID, answerText, reasoningText, cause)
}

func buildInterruptedAssistantReplayEvent(
	template *session.Event,
	activeSession session.Session,
	turnID string,
	answerText string,
	reasoningText string,
	cause error,
) *session.Event {
	event := session.CloneEvent(template)
	if event == nil {
		event = &session.Event{}
	}
	event.ID = ""
	event.Type = session.EventTypeAssistant
	event.Visibility = session.VisibilityMirror
	if event.Scope == nil {
		scope := defaultScope(activeSession, turnID)
		event.Scope = &scope
	} else {
		scope := *event.Scope
		if strings.TrimSpace(scope.TurnID) == "" {
			scope.TurnID = strings.TrimSpace(turnID)
		}
		if scope.Controller.Kind == "" {
			scope.Controller = defaultControllerRef(activeSession)
		}
		event.Scope = &scope
	}
	if event.Actor.Kind == "" {
		event.Actor = session.ActorRef{Kind: session.ActorKindController}
	}
	message := model.MessageFromAssistantParts(answerText, reasoningText, nil)
	event.Message = &message
	event.Text = message.TextContent()
	updateType := string(session.ProtocolUpdateTypeAgentMessage)
	content := interruptedAssistantReplayContent(answerText, reasoningText)
	if strings.TrimSpace(answerText) == "" {
		updateType = string(session.ProtocolUpdateTypeAgentThought)
		content = interruptedAssistantReplayContent("", reasoningText)
	}
	event.Protocol = &session.EventProtocol{
		UpdateType: updateType,
		Update: &session.ProtocolUpdate{
			SessionUpdate: updateType,
			Content:       content,
		},
	}
	event.Meta = interruptedReplayMeta(event.Meta, cause, reasoningText)
	return event
}

func interruptedAssistantReplayContent(answerText string, reasoningText string) map[string]any {
	answerText = strings.TrimSpace(answerText)
	reasoningText = strings.TrimSpace(reasoningText)
	if answerText != "" {
		return session.ProtocolTextContent(answerText)
	}
	if reasoningText != "" {
		return session.ProtocolTextContent(reasoningText)
	}
	return nil
}

func mainTurnEvent(event *session.Event, turnID string) bool {
	if event == nil || event.Scope == nil {
		return false
	}
	if strings.TrimSpace(event.Scope.TurnID) != strings.TrimSpace(turnID) {
		return false
	}
	return strings.TrimSpace(event.Scope.Participant.ID) == ""
}

func assistantEventHasReplayText(event *session.Event) bool {
	if event == nil {
		return false
	}
	if strings.TrimSpace(session.EventText(event)) != "" {
		return true
	}
	if event.Message != nil && strings.TrimSpace(event.Message.ReasoningText()) != "" {
		return true
	}
	return false
}

func assistantReplayUpdateType(event *session.Event) string {
	if event != nil && event.Protocol != nil {
		if updateType := strings.TrimSpace(event.Protocol.UpdateType); updateType != "" {
			return updateType
		}
		if event.Protocol.Update != nil {
			if updateType := strings.TrimSpace(event.Protocol.Update.SessionUpdate); updateType != "" {
				return updateType
			}
		}
	}
	return string(session.ProtocolUpdateTypeAgentMessage)
}

func interruptedReplayMeta(meta map[string]any, cause error, reasoningText string) map[string]any {
	out := maps.Clone(meta)
	if out == nil {
		out = map[string]any{}
	}
	caelis, _ := out["caelis"].(map[string]any)
	caelis = maps.Clone(caelis)
	if caelis == nil {
		caelis = map[string]any{}
	}
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	runtimeMeta = maps.Clone(runtimeMeta)
	if runtimeMeta == nil {
		runtimeMeta = map[string]any{}
	}
	replay := map[string]any{"interrupted": true}
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		replay["reason"] = cause.Error()
	}
	if reasoningText = strings.TrimSpace(reasoningText); reasoningText != "" {
		replay["reasoning_text"] = reasoningText
	}
	runtimeMeta["replay"] = replay
	caelis["runtime"] = runtimeMeta
	out["caelis"] = caelis
	return out
}
