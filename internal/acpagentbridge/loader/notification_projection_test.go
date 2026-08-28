package loader

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestProjectSessionEventNotificationsWrapsUpdatesAndAppendsUsage(t *testing.T) {
	event := &session.Event{
		SessionID: "event-session",
		Type:      session.EventTypeAssistant,
		Text:      "from updates",
		Meta: map[string]any{
			"usage": map[string]any{
				"prompt_tokens":     3,
				"completion_tokens": 4,
				"total_tokens":      7,
			},
		},
	}
	notifications, err := projectSessionEventNotifications("fallback-session", event)
	if err != nil {
		t.Fatal(err)
	}
	if len(notifications) != 2 {
		t.Fatalf("notifications = %#v, want projected update plus usage", notifications)
	}
	chunk, ok := notifications[0].Update.(eventstream.ContentChunk)
	if !ok || notifications[0].SessionID != "event-session" || session.ExtractProtocolText(chunk.Content) != "from updates" {
		t.Fatalf("notification = %#v, want event session update", notifications[0])
	}
	usage, ok := notifications[1].Update.(eventstream.UsageUpdate)
	if !ok || notifications[1].SessionID != "fallback-session" || usage.Used != 7 {
		t.Fatalf("usage notification = %#v, want fallback session usage", notifications[1])
	}
}
