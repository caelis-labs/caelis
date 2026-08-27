package loader

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

// projectSessionEventNotifications owns the session/load-specific wrapping of
// durable events as ACP notifications. Eventstream-only extensions and stored
// permission prompts are intentionally not replayed through this adapter.
func projectSessionEventNotifications(fallbackSessionID string, event *session.Event) ([]schema.SessionNotification, error) {
	updates, err := projector.ProjectEvent(event)
	if err != nil {
		return nil, err
	}
	eventSessionID := ""
	if event != nil {
		eventSessionID = strings.TrimSpace(event.SessionID)
	}
	out := make([]schema.SessionNotification, 0, len(updates)+1)
	for _, update := range updates {
		if update == nil {
			continue
		}
		out = append(out, schema.SessionNotification{
			SessionID: firstNonEmptyString(eventSessionID, fallbackSessionID),
			Update:    eventstream.CloneUpdate(update),
		})
	}
	if usage := session.UsageSnapshotFromSessionEvent(event); usage != nil && !containsUsageNotification(out) {
		out = append(out, schema.SessionNotification{
			SessionID: firstNonEmptyString(fallbackSessionID, eventSessionID),
			Update:    eventstream.UsageUpdateFromSnapshot(*usage, nil),
		})
	}
	return out, nil
}

func containsUsageNotification(notifications []schema.SessionNotification) bool {
	for _, notification := range notifications {
		if eventstream.UpdateType(notification.Update) == schema.UpdateUsage {
			return true
		}
	}
	return false
}
