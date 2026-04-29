package projector

import (
	"testing"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func TestEventProjectorNormalizesRuntimeToolStatus(t *testing.T) {
	updates, err := (EventProjector{}).ProjectEvent(&sdksession.Event{
		SessionID: "session-1",
		Type:      sdksession.EventTypeToolCall,
		Protocol: &sdksession.EventProtocol{
			UpdateType: UpdateToolCallInfo,
			ToolCall: &sdksession.ProtocolToolCall{
				ID:     "call-1",
				Name:   "SPAWN",
				Kind:   ToolKindOther,
				Status: "running",
				RawInput: map[string]any{
					"prompt": "child work",
				},
				RawOutput: map[string]any{
					"task_id": "task-1",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("ProjectEvent() produced %d updates, want 1", len(updates))
	}
	update, ok := updates[0].(ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %T, want ToolCallUpdate", updates[0])
	}
	if update.Status == nil || *update.Status != ToolStatusInProgress {
		t.Fatalf("status = %v, want %q", update.Status, ToolStatusInProgress)
	}
}

func TestEventProjectorPreservesReasoningBoundaryWhitespace(t *testing.T) {
	message := sdkmodel.NewReasoningMessage(sdkmodel.RoleAssistant, "think ", sdkmodel.ReasoningVisibilityVisible)
	updates, err := (EventProjector{}).ProjectEvent(&sdksession.Event{
		SessionID: "session-1",
		Type:      sdksession.EventTypeAssistant,
		Message:   &message,
		Text:      "think ",
		Protocol: &sdksession.EventProtocol{
			UpdateType: UpdateAgentThought,
		},
	})
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("ProjectEvent() produced %d updates, want 1", len(updates))
	}
	update, ok := updates[0].(ContentChunk)
	if !ok {
		t.Fatalf("update = %T, want ContentChunk", updates[0])
	}
	content, ok := update.Content.(TextContent)
	if !ok {
		t.Fatalf("update.Content = %T, want TextContent", update.Content)
	}
	if content.Text != "think " {
		t.Fatalf("reasoning text = %q, want boundary whitespace preserved", content.Text)
	}
}
