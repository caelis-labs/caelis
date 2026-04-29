package core

import (
	"testing"
	"time"

	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdkstream "github.com/OnslaughtSnail/caelis/sdk/stream"
)

func TestStreamRequestFromEventUsesRunningToolCursor(t *testing.T) {
	t.Parallel()

	env := EventEnvelope{
		Event: Event{
			Kind:       EventKindToolResult,
			HandleID:   "handle-1",
			RunID:      "run-1",
			TurnID:     "turn-1",
			SessionRef: sdksession.SessionRef{SessionID: "session-1"},
			Origin: &EventOrigin{
				Scope:         EventScopeMain,
				Actor:         "assistant",
				ParticipantID: "main",
			},
			ToolResult: &ToolResultPayload{
				CallID:   "call-1",
				ToolName: "BASH",
				RawInput: map[string]any{
					"command": "for i in 1 2; do echo $i; done",
				},
				RawOutput: map[string]any{
					"task_id":       "task-1",
					"terminal_id":   "terminal-1",
					"running":       true,
					"state":         "running",
					"stdout_cursor": int64(12),
					"stderr_cursor": 3,
				},
				Status:        ToolStatusRunning,
				Scope:         EventScopeMain,
				Actor:         "assistant",
				ParticipantID: "main",
			},
		},
	}

	req, ok := StreamRequestFromEvent(env)
	if !ok {
		t.Fatal("StreamRequestFromEvent() ok = false, want true")
	}
	if req.Ref.SessionID != "session-1" || req.Ref.TaskID != "task-1" || req.Ref.TerminalID != "terminal-1" {
		t.Fatalf("terminal ref = %+v", req.Ref)
	}
	if req.Cursor.Stdout != 12 || req.Cursor.Stderr != 3 {
		t.Fatalf("terminal cursor = %+v, want stdout=12 stderr=3", req.Cursor)
	}
	if req.CallID != "call-1" || req.ToolName != "BASH" {
		t.Fatalf("terminal request = %+v", req)
	}
}

func TestStreamFrameEventPreservesStandardToolUpdateShape(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		HandleID:   "handle-1",
		RunID:      "run-1",
		TurnID:     "turn-1",
		SessionRef: sdksession.SessionRef{SessionID: "session-1"},
		CallID:     "call-1",
		ToolName:   "BASH",
		RawInput:   map[string]any{"command": "echo ok"},
		Ref: sdkstream.Ref{
			SessionID:  "session-1",
			TaskID:     "task-1",
			TerminalID: "terminal-1",
		},
		Origin: &EventOrigin{Scope: EventScopeMain, Actor: "assistant"},
	}

	env := StreamFrameEvent(req, sdkstream.Frame{
		Ref:       req.Ref,
		Stream:    "stdout",
		Text:      "next line\n",
		Cursor:    sdkstream.Cursor{Stdout: 22, Stderr: 3},
		Running:   true,
		UpdatedAt: time.Unix(100, 0),
	})

	if env.Event.Kind != EventKindToolResult {
		t.Fatalf("env.Event.Kind = %q, want tool_result", env.Event.Kind)
	}
	if env.Event.ToolResult == nil {
		t.Fatal("env.Event.ToolResult = nil")
	}
	if env.Event.ToolResult.CallID != "call-1" || env.Event.ToolResult.ToolName != "BASH" {
		t.Fatalf("tool result = %+v", env.Event.ToolResult)
	}
	output := env.Event.ToolResult.RawOutput
	if output["text"] != "next line\n" || output["stream"] != "stdout" {
		t.Fatalf("raw output = %#v", output)
	}
	if output["stdout_cursor"] != int64(22) || output["stderr_cursor"] != int64(3) {
		t.Fatalf("raw output cursor = %#v", output)
	}
	caelis, ok := env.Event.Meta["caelis"].(map[string]any)
	if !ok {
		t.Fatalf("env.Event.Meta = %#v, want meta.caelis", env.Event.Meta)
	}
	if transient, _ := caelis["transient"].(bool); !transient {
		t.Fatalf("meta.caelis = %#v, want transient=true", caelis)
	}
}
