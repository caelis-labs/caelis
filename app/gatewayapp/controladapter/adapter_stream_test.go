package controladapter

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestStreamRequestFromACPEventAcceptsInProgressTaskRefWithoutRunningFlag(t *testing.T) {
	status := schema.ToolStatusInProgress
	kind := "RUN_COMMAND"
	req, ok := streamRequestFromACPEvent(eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		HandleID:  "handle-1",
		RunID:     "run-1",
		TurnID:    "turn-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Kind:          &kind,
			Status:        &status,
			RawInput:      map[string]any{"command": "sleep 10"},
			Meta: map[string]any{
				"caelis": map[string]any{
					"runtime": map[string]any{
						"task": map[string]any{
							"task_id":       "task-1",
							"terminal_id":   "terminal-1",
							"output_cursor": int64(12),
						},
					},
				},
			},
		},
	})
	if !ok {
		t.Fatal("streamRequestFromACPEvent() ok = false, want true")
	}
	if req.Ref.SessionID != "session-1" || req.Ref.TaskID != "task-1" || req.Ref.TerminalID != "terminal-1" {
		t.Fatalf("stream ref = %+v, want session/task/terminal ids", req.Ref)
	}
	if req.Cursor.Output != 12 {
		t.Fatalf("cursor = %+v, want output=12", req.Cursor)
	}
	if req.CallID != "call-1" || req.ToolName != "RUN_COMMAND" {
		t.Fatalf("request = %+v, want RUN_COMMAND call-1", req)
	}
}
