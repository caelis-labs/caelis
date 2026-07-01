package local

import (
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/compact"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func buildCompactEvent(activeSession session.Session, compactText string, data compact.CompactEventData) *session.Event {
	message := model.NewTextMessage(model.RoleUser, normalizeCompactMarkdown(compactText))
	scope := defaultScope(activeSession, "")
	return &session.Event{
		Type:       session.EventTypeCompact,
		Visibility: session.VisibilityCanonical,
		Actor:      session.ActorRef{Kind: session.ActorKindSystem, Name: "runtime"},
		Scope:      &scope,
		Message:    &message,
		Text:       message.TextContent(),
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodContextCheckpoint,
			Update: &session.ProtocolUpdate{
				SessionUpdate: "compact",
				Content:       session.ProtocolTextContent(message.TextContent()),
			},
		},
		Meta: map[string]any{
			compact.MetaKeyCompact: compact.CompactEventDataValue(data),
		},
	}
}

func buildCompactNoticeEvent(activeSession session.Session, turnID string, occurredAt time.Time) *session.Event {
	scope := defaultScope(activeSession, turnID)
	event := &session.Event{
		Type:  session.EventTypeNotice,
		Time:  occurredAt,
		Actor: session.ActorRef{Kind: session.ActorKindSystem, Name: "runtime"},
		Scope: &scope,
	}
	return session.MarkNotice(event, "notice", compact.CompactNoticeLabel)
}

func compactTextFromEvent(event *session.Event) string {
	if event == nil {
		return ""
	}
	return strings.TrimSpace(session.EventText(event))
}

func compactableEvents(events []*session.Event) []*session.Event {
	return compact.EventsAfterLatestCompact(events)
}

func compactableEventCount(events []*session.Event) int {
	return len(compactableEvents(events))
}

func eventTextForCompaction(event *session.Event) string {
	if event == nil {
		return ""
	}
	if text := strings.TrimSpace(session.EventText(event)); text != "" {
		return text
	}
	return ""
}

func pendingEventsForCompaction(event *session.Event) []*session.Event {
	if event == nil || !session.IsMainInvocationVisibleEvent(event) {
		return nil
	}
	return []*session.Event{session.CloneEvent(event)}
}

func promptEventsWithPending(promptEvents []*session.Event, pendingEvents []*session.Event) []*session.Event {
	if len(pendingEvents) == 0 {
		return promptEvents
	}
	out := make([]*session.Event, 0, len(promptEvents)+len(pendingEvents))
	out = append(out, promptEvents...)
	for _, event := range pendingEvents {
		if event == nil || !session.IsMainInvocationVisibleEvent(event) {
			continue
		}
		out = append(out, event)
	}
	return out
}

func mainInvocationEvents(events []*session.Event) []*session.Event {
	if len(events) == 0 {
		return events
	}
	out := make([]*session.Event, 0, len(events))
	for _, event := range events {
		if !session.IsMainInvocationVisibleEvent(event) {
			continue
		}
		out = append(out, event)
	}
	return out
}
