// Package projector owns canonical session.Event to ACP update projection.
package projector

import (
	"github.com/OnslaughtSnail/caelis/acp"
	"github.com/OnslaughtSnail/caelis/session"
)

type Update = acp.Update
type SessionNotification = acp.SessionNotification

// EventProjector projects canonical SDK session events to ACP updates.
type EventProjector struct{}

func (EventProjector) ProjectEvent(event *session.Event) []Update {
	return acp.ProjectEvent(event)
}

func (p EventProjector) ProjectNotifications(sessionID string, event *session.Event) []SessionNotification {
	updates := p.ProjectEvent(event)
	if len(updates) == 0 {
		return nil
	}
	out := make([]SessionNotification, 0, len(updates))
	for _, update := range updates {
		out = append(out, acp.ProjectToNotification(sessionID, update))
	}
	return out
}

func ProjectEvent(event *session.Event) []Update {
	return EventProjector{}.ProjectEvent(event)
}

func ProjectNotifications(sessionID string, event *session.Event) []SessionNotification {
	return EventProjector{}.ProjectNotifications(sessionID, event)
}
