package controladapter

import (
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestStreamRequestFromACPEventAcceptsInProgressTaskRefWithoutRunningFlag(t *testing.T) {
	t.Parallel()

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
	if req.DisplayTerminalID != "call-1" {
		t.Fatalf("display terminal id = %q, want tool call id fallback", req.DisplayTerminalID)
	}
}

func TestStreamRequestFromACPEventDerivesStreamToolFromStandardTitle(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		title    string
		wantTool string
	}{
		{
			name:     "run command",
			title:    "RUN_COMMAND sleep 10",
			wantTool: "RunCommand",
		},
		{
			name:     "spawn",
			title:    "SPAWN reviewer: inspect",
			wantTool: "Spawn",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			status := schema.ToolStatusInProgress
			kind := schema.ToolKindExecute
			title := tt.title
			req, ok := streamRequestFromACPEvent(eventstream.Envelope{
				Kind:      eventstream.KindSessionUpdate,
				SessionID: "session-1",
				Update: schema.ToolCallUpdate{
					SessionUpdate: schema.UpdateToolCallInfo,
					ToolCallID:    "call-1",
					Title:         &title,
					Kind:          &kind,
					Status:        &status,
					Meta: map[string]any{
						"caelis": map[string]any{
							"runtime": map[string]any{
								"task": map[string]any{
									"task_id":     "task-1",
									"terminal_id": "terminal-1",
								},
							},
						},
					},
				},
			})
			if !ok {
				t.Fatal("streamRequestFromACPEvent() ok = false, want true")
			}
			if req.ToolName != tt.wantTool {
				t.Fatalf("tool name = %q, want %q", req.ToolName, tt.wantTool)
			}
		})
	}
}

func TestStreamRequestFromACPEventAcceptsStandardExecuteTerminalUpdate(t *testing.T) {
	t.Parallel()

	status := schema.ToolStatusInProgress
	kind := schema.ToolKindExecute
	title := "Run tests"
	req, ok := streamRequestFromACPEvent(eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Title:         &title,
			Kind:          &kind,
			Status:        &status,
			Meta: map[string]any{
				"caelis": map[string]any{
					"runtime": map[string]any{
						"task": map[string]any{
							"task_id":     "task-1",
							"terminal_id": "terminal-1",
						},
					},
				},
			},
		},
	})
	if !ok {
		t.Fatal("streamRequestFromACPEvent() ok = false, want true for ACP execute terminal update")
	}
	if req.ToolName != schema.ToolKindExecute {
		t.Fatalf("tool name = %q, want ACP kind", req.ToolName)
	}
}

func TestStreamRequestFromACPEventTerminalIDPrecedence(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name              string
		metaTerminalID    string
		contentTerminalID string
		wantTerminalID    string
	}{
		{
			name:              "meta task terminal wins",
			metaTerminalID:    "meta-terminal",
			contentTerminalID: "content-terminal",
			wantTerminalID:    "meta-terminal",
		},
		{
			name:              "content terminal fallback",
			contentTerminalID: "content-terminal",
			wantTerminalID:    "content-terminal",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			status := schema.ToolStatusInProgress
			kind := "RUN_COMMAND"
			taskMeta := map[string]any{"task_id": "task-1"}
			if tt.metaTerminalID != "" {
				taskMeta["terminal_id"] = tt.metaTerminalID
			}
			req, ok := streamRequestFromACPEvent(eventstream.Envelope{
				Kind:      eventstream.KindSessionUpdate,
				SessionID: "session-1",
				Update: schema.ToolCallUpdate{
					SessionUpdate: schema.UpdateToolCallInfo,
					ToolCallID:    "call-1",
					Kind:          &kind,
					Status:        &status,
					Content: []schema.ToolCallContent{{
						Type:       "terminal",
						TerminalID: tt.contentTerminalID,
					}},
					Meta: map[string]any{
						"caelis": map[string]any{
							"runtime": map[string]any{
								"task": taskMeta,
							},
						},
					},
				},
			})
			if !ok {
				t.Fatal("streamRequestFromACPEvent() ok = false, want true")
			}
			if req.Ref.TerminalID != tt.wantTerminalID {
				t.Fatalf("terminal id = %q, want %q", req.Ref.TerminalID, tt.wantTerminalID)
			}
			if tt.contentTerminalID != "" && req.DisplayTerminalID != tt.contentTerminalID {
				t.Fatalf("display terminal id = %q, want content terminal id %q", req.DisplayTerminalID, tt.contentTerminalID)
			}
		})
	}
}
