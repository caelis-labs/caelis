package runtime

import (
	"errors"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
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

func buildCompactFailureNoticeEvent(activeSession session.Session, turnID string, occurredAt time.Time, cause error) *session.Event {
	scope := defaultScope(activeSession, turnID)
	event := &session.Event{
		Type:  session.EventTypeNotice,
		Time:  occurredAt,
		Actor: session.ActorRef{Kind: session.ActorKindSystem, Name: "runtime"},
		Scope: &scope,
	}
	event = session.MarkNotice(event, "warning", compactFailureNoticeText(cause))
	if event != nil && event.Notice != nil {
		event.Notice.Kind = "compact_failed"
	}
	return event
}

func compactFailureNoticeText(cause error) string {
	text := strings.TrimSpace(compactionFailureCauseText(cause))
	if text == "" {
		return compact.CompactFailureLabel
	}
	return compact.CompactFailureLabel + ": " + truncateCompactFailureNotice(text)
}

func compactionFailureCauseText(cause error) string {
	if cause == nil {
		return ""
	}
	var compactErr *compactionFailureError
	if errors.As(cause, &compactErr) && compactErr != nil && compactErr.cause != nil {
		return compactErr.cause.Error()
	}
	return cause.Error()
}

func truncateCompactFailureNotice(text string) string {
	const limit = 600
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= limit {
		return text
	}
	return strings.TrimSpace(text[:limit]) + "..."
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
