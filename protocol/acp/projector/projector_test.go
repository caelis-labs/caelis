package projector

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func TestEventProjectorNormalizesRuntimeToolStatus(t *testing.T) {
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolCall,
		Protocol: &session.EventProtocol{
			UpdateType: UpdateToolCallInfo,
			ToolCall: &session.ProtocolToolCall{
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

func TestEventProjectorProjectsSpawnAsExecuteWithTerminalContent(t *testing.T) {
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolCall,
		Protocol: &session.EventProtocol{
			UpdateType: UpdateToolCallInfo,
			ToolCall: &session.ProtocolToolCall{
				ID:     "call-1",
				Name:   "SPAWN",
				Status: "running",
				RawInput: map[string]any{
					"agent":  "codex",
					"prompt": "child work",
				},
				RawOutput: map[string]any{
					"task_id":     "task-1",
					"terminal_id": "terminal-1",
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
	if update.Kind == nil || *update.Kind != ToolKindExecute {
		t.Fatalf("kind = %v, want %q", update.Kind, ToolKindExecute)
	}
	if update.Title == nil || *update.Title != "SPAWN codex" {
		t.Fatalf("title = %v, want SPAWN codex", update.Title)
	}
	if len(update.Content) != 1 || update.Content[0].Type != "terminal" || update.Content[0].TerminalID != "terminal-1" {
		t.Fatalf("content = %#v, want terminal content for terminal-1", update.Content)
	}
}

func TestEventProjectorProjectsBashDisplayTerminalMetadata(t *testing.T) {
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolCall,
		Protocol: &session.EventProtocol{
			UpdateType: UpdateToolCall,
			ToolCall: &session.ProtocolToolCall{
				ID:     "call-1",
				Name:   "BASH",
				Status: "pending",
				RawInput: map[string]any{
					"command": "echo hi",
					"workdir": "/tmp/work",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("ProjectEvent() produced %d updates, want tool_call only", len(updates))
	}
	call, ok := updates[0].(ToolCall)
	if !ok {
		t.Fatalf("updates[0] = %T, want ToolCall", updates[0])
	}
	if call.Kind != ToolKindExecute {
		t.Fatalf("kind = %q, want %q", call.Kind, ToolKindExecute)
	}
	if len(call.Content) != 1 || call.Content[0].Type != "terminal" || call.Content[0].TerminalID != "call-1" {
		t.Fatalf("content = %#v, want display terminal content for call-1", call.Content)
	}
	info, ok := call.Meta["terminal_info"].(map[string]any)
	if !ok {
		t.Fatalf("meta = %#v, want terminal_info", call.Meta)
	}
	if info["terminal_id"] != "call-1" || info["cwd"] != "/tmp/work" {
		t.Fatalf("terminal_info = %#v, want terminal_id call-1 cwd /tmp/work", info)
	}
}

func TestEventProjectorPreservesReasoningBoundaryWhitespace(t *testing.T) {
	message := model.NewReasoningMessage(model.RoleAssistant, "think ", model.ReasoningVisibilityVisible)
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeAssistant,
		Message:   &message,
		Text:      "think ",
		Protocol: &session.EventProtocol{
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
