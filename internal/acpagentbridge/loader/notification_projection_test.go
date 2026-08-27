package loader

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestProjectSessionEventNotificationsWrapsUpdatesAndAppendsUsage(t *testing.T) {
	event := &session.Event{
		SessionID: "event-session",
		Type:      session.EventTypeAssistant,
		Meta: map[string]any{
			"usage": map[string]any{
				"prompt_tokens":     3,
				"completion_tokens": 4,
				"total_tokens":      7,
			},
		},
	}
	notifications, err := projectSessionEventNotifications("fallback-session", event, notificationOverrideProjector{})
	if err != nil {
		t.Fatal(err)
	}
	if len(notifications) != 2 {
		t.Fatalf("notifications = %#v, want custom update plus usage", notifications)
	}
	chunk, ok := notifications[0].Update.(schema.ContentChunk)
	if !ok || notifications[0].SessionID != "event-session" || schema.ExtractTextValue(chunk.Content) != "from updates" {
		t.Fatalf("custom notification = %#v, want event session update", notifications[0])
	}
	usage, ok := notifications[1].Update.(schema.UsageUpdate)
	if !ok || notifications[1].SessionID != "fallback-session" || usage.Used != 7 {
		t.Fatalf("usage notification = %#v, want fallback session usage", notifications[1])
	}
}

func TestProjectSessionEventNotificationsDoesNotDuplicateProjectedUsage(t *testing.T) {
	event := &session.Event{
		SessionID: "event-session",
		Meta:      map[string]any{"usage": map[string]any{"total_tokens": 7}},
	}
	notifications, err := projectSessionEventNotifications("fallback-session", event, usageNotificationProjector{})
	if err != nil {
		t.Fatal(err)
	}
	if len(notifications) != 1 || notifications[0].SessionID != "event-session" || notifications[0].Update.SessionUpdateType() != schema.UpdateUsage {
		t.Fatalf("notifications = %#v, want one projected usage update", notifications)
	}
}

type notificationOverrideProjector struct{}

func (notificationOverrideProjector) ProjectEvent(*session.Event) ([]schema.Update, error) {
	return []schema.Update{schema.ContentChunk{
		SessionUpdate: schema.UpdateAgentMessage,
		Content:       schema.TextContent{Type: "text", Text: "from updates"},
	}}, nil
}

func (notificationOverrideProjector) ProjectPermissionRequest(*session.Event) (*schema.RequestPermissionRequest, bool, error) {
	return nil, false, nil
}

type usageNotificationProjector struct{}

func (usageNotificationProjector) ProjectEvent(*session.Event) ([]schema.Update, error) {
	return []schema.Update{schema.UsageUpdate{SessionUpdate: schema.UpdateUsage, Size: 7, Used: 7}}, nil
}

func (usageNotificationProjector) ProjectPermissionRequest(*session.Event) (*schema.RequestPermissionRequest, bool, error) {
	return nil, false, nil
}
