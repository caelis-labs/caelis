package loader

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"
)

func TestSpawnReplayDropsStaleSuccessWhenFailureHasNoReason(t *testing.T) {
	interrupted := "interrupted"
	update := withSpawnReplayResult(eventstream.ToolCallUpdate{
		SessionUpdate: eventstream.UpdateToolCallInfo,
		ToolCallID:    "spawn-alpha",
		Status:        &interrupted,
		RawOutput: map[string]any{
			"state": "cancelled", "turn_id": "turn-1", "final_message": "stale completed final",
		},
		Content: []eventstream.ToolCallContent{{
			Type: "content", Content: eventstream.TextContent{Type: "text", Text: "stale completed final"},
		}},
		Meta: acpmeta.WithTerminalOutput(nil, "spawn-alpha", "stale completed final"),
	}, map[string]any{
		"state": "cancelled", "turn_id": "turn-1", "final_message": "stale completed final",
	})
	if update.Status == nil || *update.Status != eventstream.ToolStatusFailed || len(update.Content) != 0 || update.Meta != nil {
		t.Fatalf("failed replay = %#v, want failed status without stale success output", update)
	}
}

func TestSpawnReplayNormalizesCanonicalTerminalStatus(t *testing.T) {
	interrupted := "interrupted"
	event := &session.Event{
		Type: session.EventTypeToolResult,
		Tool: &session.EventTool{
			ID: "spawn-alpha", Name: "Spawn", Status: interrupted,
			Output: map[string]any{
				"state": "cancelled", "turn_id": "turn-1", "final_message": "stale completed final", "error": "cancelled by parent",
			},
		},
	}
	replay := newSpawnReplayProjector([]*session.Event{event})
	notification := replay.normalize(event, eventstream.SessionNotification{
		SessionID: "session-1",
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "spawn-alpha", Status: &interrupted,
			RawOutput: event.Tool.Output,
			Content: []eventstream.ToolCallContent{{
				Type: "content", Content: eventstream.TextContent{Type: "text", Text: "stale completed final"},
			}},
		},
	})
	update := notification.Update.(eventstream.ToolCallUpdate)
	if update.Status == nil || *update.Status != eventstream.ToolStatusFailed {
		t.Fatalf("replayed status = %#v, want ACP failed", update.Status)
	}
	if len(update.Content) != 1 {
		t.Fatalf("replayed failed content = %#v, want one standard result", update.Content)
	}
	text, ok := update.Content[0].Content.(eventstream.TextContent)
	if !ok || text.Text != "cancelled by parent" {
		t.Fatalf("replayed failed content = %#v, want cancellation reason", update.Content)
	}
}
