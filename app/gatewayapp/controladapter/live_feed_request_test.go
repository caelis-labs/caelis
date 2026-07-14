package controladapter

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
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
	if req.Cursor != (stream.Cursor{}) {
		t.Fatalf("cursor = %+v, want replay-safe zero because the running snapshot is not rendered", req.Cursor)
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

func TestStreamRequestFromACPEventSeparatesPhysicalChildSourceFromObserverCall(t *testing.T) {
	t.Parallel()

	spawn := brokerRunningSubagentEnvelope(
		"SPAWN", "spawn-call-1", "", "task-1", "spawn-terminal-1", "task-1:1", 7,
	)
	wait := brokerRunningSubagentEnvelope(
		"TASK", "task-wait-1", "wait", "task-1", "spawn-terminal-1", "task-1:1", 7,
	)
	write := brokerRunningSubagentEnvelope(
		"TASK", "task-write-1", "write", "task-1", "spawn-terminal-1", "task-1:2", 3,
	)

	spawnRequest, ok := streamRequestFromACPEvent(spawn)
	if !ok {
		t.Fatal("Spawn stream request was not recognized")
	}
	waitRequest, ok := streamRequestFromACPEvent(wait)
	if !ok {
		t.Fatal("TASK wait stream request was not recognized")
	}
	writeRequest, ok := streamRequestFromACPEvent(write)
	if !ok {
		t.Fatal("TASK write stream request was not recognized")
	}

	if spawnRequest.Key() != waitRequest.Key() {
		t.Fatalf("Spawn/TASK wait physical keys differ: %q != %q", spawnRequest.Key(), waitRequest.Key())
	}
	if !waitRequest.Observer || waitRequest.Cursor != (stream.Cursor{}) {
		t.Fatalf("TASK wait request = %#v, want observer from replay-safe cursor zero", waitRequest)
	}
	if waitRequest.ParentCallID != "spawn-call-1" || waitRequest.ParentToolName != "SPAWN" {
		t.Fatalf("TASK wait parent = (%q, %q), want canonical Spawn", waitRequest.ParentCallID, waitRequest.ParentToolName)
	}
	if spawnRequest.Observer || spawnRequest.Cursor.Events != 0 {
		t.Fatalf("Spawn owner request = %#v, want source owner from event cursor zero", spawnRequest)
	}
	if writeRequest.Observer || writeRequest.Cursor.Events != 0 || writeRequest.Key() == spawnRequest.Key() {
		t.Fatalf("TASK write request = %#v, want distinct continuation source from event cursor zero", writeRequest)
	}
}

func TestStreamRequestFromACPEventRoutesCommandWaitToRunCommandSource(t *testing.T) {
	t.Parallel()

	wait := brokerRunningCommandWaitEnvelope(
		"task-wait-1", "command-task-1", "command-terminal-1", "run-command-call-1", 37,
	)
	request, ok := streamRequestFromACPEvent(wait)
	if !ok {
		t.Fatal("command TASK wait stream request was not recognized")
	}
	if !request.Observer || request.TargetKind != task.KindCommand || request.Cursor != (stream.Cursor{}) {
		t.Fatalf("command TASK wait = %#v, want command observer from replay-safe cursor zero", request)
	}
	if request.ParentCallID != "run-command-call-1" || request.ParentToolName != "RunCommand" {
		t.Fatalf("command TASK wait parent = (%q, %q), want original RunCommand", request.ParentCallID, request.ParentToolName)
	}
}
