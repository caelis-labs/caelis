package kernel

import (
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
)

func TestStreamRequestFromEventUsesRunningToolCursor(t *testing.T) {
	t.Parallel()

	env := EventEnvelope{
		Event: Event{
			Kind:       EventKindToolResult,
			HandleID:   "handle-1",
			RunID:      "run-1",
			TurnID:     "turn-1",
			SessionRef: session.SessionRef{SessionID: "session-1"},
			Origin: &EventOrigin{
				Scope:         EventScopeMain,
				Actor:         "assistant",
				ParticipantID: "main",
			},
			Meta: map[string]any{
				"caelis": map[string]any{
					"runtime": map[string]any{
						"task": map[string]any{
							"terminal_id":   "terminal-1",
							"task_id":       "task-1",
							"output_cursor": int64(12),
						},
					},
				},
			},
			ToolResult: &ToolResultPayload{
				CallID:   "call-1",
				ToolName: "BASH",
				RawInput: map[string]any{
					"command": "for i in 1 2; do echo $i; done",
				},
				Content:       []session.ProtocolToolCallContent{{Type: "terminal", TerminalID: "terminal-1"}},
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
	if req.Cursor.Output != 12 {
		t.Fatalf("terminal cursor = %+v, want output=12", req.Cursor)
	}
	if req.CallID != "call-1" || req.ToolName != "BASH" {
		t.Fatalf("terminal request = %+v", req)
	}
}

func TestStreamRequestFromEventIgnoresRunningTaskControl(t *testing.T) {
	t.Parallel()

	_, ok := StreamRequestFromEvent(EventEnvelope{
		Event: Event{
			Kind:       EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "session-1"},
			ToolResult: &ToolResultPayload{
				CallID:   "task-write-1",
				ToolName: "TASK",
				Status:   ToolStatusRunning,
				RawInput: map[string]any{
					"action":  "write",
					"task_id": "spawn-1",
					"input":   "continue",
				},
				Content: []session.ProtocolToolCallContent{{Type: "terminal", TerminalID: "spawn-1"}},
			},
			Meta: map[string]any{
				"caelis": map[string]any{
					"runtime": map[string]any{
						"task": map[string]any{
							"task_id":     "spawn-1",
							"terminal_id": "spawn-1",
							"running":     true,
						},
					},
				},
			},
		},
	})
	if ok {
		t.Fatal("StreamRequestFromEvent(TASK running) ok = true, want false")
	}
}

func TestStreamFrameEventsDoNotAppendReasoningTextToParentTool(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		SessionRef: session.SessionRef{SessionID: "root-session"},
		CallID:     "spawn-1",
		ToolName:   "SPAWN",
		RawInput:   map[string]any{"agent": "self", "prompt": "demo"},
		Ref:        stream.Ref{SessionID: "root-session", TaskID: "amy"},
		Scope:      EventScopeMain,
	}
	events := StreamFrameEvents(req, stream.Frame{
		Ref:     req.Ref,
		Text:    "The user wants me to inspect files.",
		Running: true,
		Event: &session.Event{
			Type: session.EventTypeAssistant,
			Protocol: &session.EventProtocol{
				UpdateType: string(session.ProtocolUpdateTypeAgentThought),
				Update:     &session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeAgentThought)},
			},
		},
	})
	for _, event := range events {
		if event.Event.Kind == EventKindToolResult {
			t.Fatalf("reasoning frame events = %#v, should not append parent tool update", events)
		}
	}

	events = StreamFrameEvents(req, stream.Frame{
		Ref:     req.Ref,
		Text:    "final visible output",
		Running: true,
	})
	if len(events) != 1 || events[0].Event.Kind != EventKindToolResult {
		t.Fatalf("stdout frame events = %#v, want one parent tool update", events)
	}
}

func TestStreamFrameEventPreservesStandardToolUpdateShape(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		HandleID:   "handle-1",
		RunID:      "run-1",
		TurnID:     "turn-1",
		SessionRef: session.SessionRef{SessionID: "session-1"},
		CallID:     "call-1",
		ToolName:   "BASH",
		RawInput:   map[string]any{"command": "echo ok"},
		Ref: stream.Ref{
			SessionID:  "session-1",
			TaskID:     "task-1",
			TerminalID: "terminal-1",
		},
		Origin: &EventOrigin{Scope: EventScopeMain, Actor: "assistant"},
	}

	env := StreamFrameEvent(req, stream.Frame{
		Ref:       req.Ref,
		Text:      "next line\n",
		Cursor:    stream.Cursor{Output: 22},
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
	if got := sessionTextContent(t, env.Event.ToolResult.Content); got != "next line\n" {
		t.Fatalf("content text = %q, want stream delta", got)
	}
	caelis, ok := env.Event.Meta["caelis"].(map[string]any)
	if !ok {
		t.Fatalf("env.Event.Meta = %#v, want meta.caelis", env.Event.Meta)
	}
	if transient, _ := caelis["transient"].(bool); !transient {
		t.Fatalf("meta.caelis = %#v, want transient=true", caelis)
	}
}

func TestStreamFrameEventsProjectTaskClosedFrameWithoutText(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		SessionRef: session.SessionRef{SessionID: "root-session"},
		CallID:     "task-write-1",
		ToolName:   "TASK",
		RawInput:   map[string]any{"action": "write", "task_id": "maya", "input": "continue"},
		Ref:        stream.Ref{SessionID: "root-session", TaskID: "maya"},
		Scope:      EventScopeMain,
	}
	events := StreamFrameEvents(req, stream.Frame{
		Ref:       stream.Ref{SessionID: "root-session", TaskID: "internal-task"},
		Text:      "已追加",
		Closed:    true,
		Running:   false,
		State:     "completed",
		UpdatedAt: time.Unix(180, 0),
	})
	if len(events) != 1 {
		t.Fatalf("StreamFrameEvents(TASK closed) returned %d events: %#v", len(events), events)
	}
	payload := events[0].Event.ToolResult
	if payload == nil {
		t.Fatalf("event = %#v, want tool result", events[0].Event)
	}
	if payload.CallID != "task-write-1" || payload.ToolName != "TASK" || payload.Status != ToolStatusCompleted {
		t.Fatalf("payload = %+v, want completed TASK result", payload)
	}
	if got := sessionTextContent(t, payload.Content); got != "已追加" {
		t.Fatalf("content text = %q, want final TASK text", got)
	}
}

func TestStreamFrameEventsUseNoOutputPlaceholderForSilentBashFailure(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		SessionRef: session.SessionRef{SessionID: "root-session"},
		CallID:     "bash-1",
		ToolName:   "BASH",
		RawInput:   map[string]any{"command": "false"},
		Ref:        stream.Ref{SessionID: "root-session", TaskID: "task-1", TerminalID: "terminal-1"},
		Scope:      EventScopeMain,
	}
	events := StreamFrameEvents(req, stream.Frame{
		Ref:     stream.Ref{SessionID: "root-session", TaskID: "task-1", TerminalID: "terminal-1"},
		Closed:  true,
		Running: false,
		State:   "failed",
	})
	if len(events) != 1 {
		t.Fatalf("StreamFrameEvents(BASH closed) returned %d events: %#v", len(events), events)
	}
	payload := events[0].Event.ToolResult
	if payload == nil || payload.Status != ToolStatusFailed {
		t.Fatalf("payload = %+v, want failed BASH result", payload)
	}
	if strings.Contains(sessionTextContent(t, payload.Content), "exit 1") {
		t.Fatalf("content = %#v, should not expose exit code as terminal output", payload.Content)
	}
	if got := sessionTextContent(t, payload.Content); got != "(no output)" {
		t.Fatalf("content text = %q, want no-output placeholder", got)
	}
}

func TestStreamFrameEventsProjectBashClosedFrameAsContentlessFinalAfterOutput(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		SessionRef: session.SessionRef{SessionID: "root-session"},
		CallID:     "bash-1",
		ToolName:   "BASH",
		RawInput:   map[string]any{"command": "printf hi"},
		Ref:        stream.Ref{SessionID: "root-session", TaskID: "task-1", TerminalID: "terminal-1"},
		Scope:      EventScopeMain,
	}
	events := StreamFrameEvents(req, stream.Frame{
		Ref:     stream.Ref{SessionID: "root-session", TaskID: "task-1", TerminalID: "terminal-1"},
		Closed:  true,
		Running: false,
		State:   "completed",
		Cursor:  stream.Cursor{Output: int64(len("hi"))},
	})
	if len(events) != 1 {
		t.Fatalf("StreamFrameEvents(BASH closed) returned %d events: %#v", len(events), events)
	}
	payload := events[0].Event.ToolResult
	if payload == nil || payload.Status != ToolStatusCompleted {
		t.Fatalf("payload = %+v, want completed BASH result", payload)
	}
	if len(payload.Content) != 0 {
		t.Fatalf("content = %#v, want contentless final after streamed output", payload.Content)
	}
}

func TestStreamFrameEventsPreserveEmbeddedSubagentEventAndToolUpdate(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		HandleID:   "handle-1",
		RunID:      "run-1",
		TurnID:     "turn-1",
		SessionRef: session.SessionRef{SessionID: "root-session"},
		CallID:     "spawn-call-1",
		ToolName:   "SPAWN",
		RawInput:   map[string]any{"agent": "self", "prompt": "inspect"},
		Ref: stream.Ref{
			SessionID: "root-session",
			TaskID:    "jack",
		},
		Origin: &EventOrigin{Scope: EventScopeMain, ScopeID: "root-session", Actor: "assistant"},
		Scope:  EventScopeMain,
	}
	frame := stream.Frame{
		Ref:       req.Ref,
		Text:      "The user wants a file",
		Cursor:    stream.Cursor{Output: 21, Events: 1},
		Running:   true,
		UpdatedAt: time.Unix(200, 0),
		Event: &session.Event{
			ID:         "child-event-1",
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical,
			Text:       "The user wants a file",
			Scope: &session.EventScope{
				Participant: session.ParticipantRef{
					ID:           "self-1",
					Kind:         session.ParticipantKindSubagent,
					Role:         session.ParticipantRoleDelegated,
					DelegationID: "jack",
				},
				ACP: session.ACPRef{SessionID: "child-session"},
			},
		},
	}

	events := StreamFrameEvents(req, frame)
	if len(events) != 2 {
		t.Fatalf("StreamFrameEvents() returned %d events, want embedded child event and tool update: %#v", len(events), events)
	}
	child := events[0].Event
	if child.Kind != EventKindAssistantMessage || child.Narrative == nil || child.Narrative.Text != "The user wants a file" {
		t.Fatalf("child event = %#v, want assistant narrative from frame.Event", child)
	}
	if child.Origin == nil || child.Origin.Scope != EventScopeSubagent || child.Origin.ScopeID != "jack" {
		t.Fatalf("child origin = %#v, want subagent scope keyed by SPAWN task", child.Origin)
	}
	if child.Origin.ParticipantSessionID != "child-session" {
		t.Fatalf("child origin participant session = %q, want original ACP child session", child.Origin.ParticipantSessionID)
	}
	tool := events[1].Event
	if tool.Kind != EventKindToolResult || tool.ToolResult == nil {
		t.Fatalf("tool event = %#v, want stream tool result", tool)
	}
	if got := sessionTextContent(t, tool.ToolResult.Content); got != "The user wants a file" {
		t.Fatalf("tool content = %q, want original stream text", got)
	}

	eventOnly := frame
	eventOnly.Text = ""
	events = StreamFrameEvents(req, eventOnly)
	if len(events) != 1 || events[0].Event.Kind != EventKindAssistantMessage {
		t.Fatalf("StreamFrameEvents(event-only) = %#v, want embedded child event even without stream text", events)
	}
}

func TestStreamFrameEventsPreferRequestTaskIDForSpawnRunningToolUpdate(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		SessionRef: session.SessionRef{SessionID: "root-session"},
		CallID:     "spawn-call-1",
		ToolName:   "SPAWN",
		RawInput:   map[string]any{"agent": "self", "prompt": "continue"},
		Ref:        stream.Ref{SessionID: "root-session", TaskID: "maya"},
		Scope:      EventScopeMain,
	}
	events := StreamFrameEvents(req, stream.Frame{
		Ref:     stream.Ref{SessionID: "root-session", TaskID: "internal-task"},
		Text:    "live continuation output",
		Running: true,
	})
	if len(events) != 1 {
		t.Fatalf("StreamFrameEvents() returned %d events: %#v", len(events), events)
	}
	if got := EventMetaString(events[0].Event.Meta, "caelis", "runtime", "tool", EventMetaRuntimeTargetID); got != "maya" {
		t.Fatalf("tool meta target = %q, want visible task id from stream request", got)
	}
}

func TestStreamFrameEventsProjectSubagentClosedFrameAsCleanFinalToolResult(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		HandleID:   "handle-1",
		RunID:      "run-1",
		TurnID:     "turn-1",
		SessionRef: session.SessionRef{SessionID: "root-session"},
		CallID:     "spawn-call-1",
		ToolName:   "SPAWN",
		RawInput:   map[string]any{"agent": "self", "prompt": "inspect"},
		Ref:        stream.Ref{SessionID: "root-session", TaskID: "jack"},
		Origin:     &EventOrigin{Scope: EventScopeMain, Actor: "assistant"},
		Scope:      EventScopeMain,
	}
	events := StreamFrameEvents(req, stream.Frame{
		Ref:       stream.Ref{SessionID: "root-session", TaskID: "task-internal"},
		Text:      "### 已完成\n- `hello_from_spawn.txt` 内容正确\n| 文件 | 状态 |\n| --- | --- |\n| `hello_from_spawn.txt` | **created** |",
		Closed:    true,
		Running:   false,
		State:     "completed",
		UpdatedAt: time.Unix(300, 0),
	})
	if len(events) != 1 {
		t.Fatalf("StreamFrameEvents(closed) returned %d events: %#v", len(events), events)
	}
	payload := events[0].Event.ToolResult
	if payload == nil {
		t.Fatalf("event = %#v, want tool result", events[0].Event)
	}
	if payload.Status != ToolStatusCompleted || payload.Error {
		t.Fatalf("status/error = %q/%v, want completed false", payload.Status, payload.Error)
	}
	if payload.CallID != "spawn-call-1" || payload.ToolName != "SPAWN" {
		t.Fatalf("payload = %+v, want parent SPAWN call", payload)
	}
	result := sessionTextContent(t, payload.Content)
	for _, want := range []string{"已完成", "hello_from_spawn.txt 内容正确", "文件  状态", "hello_from_spawn.txt  created"} {
		if !strings.Contains(result, want) {
			t.Fatalf("clean result = %q, want %q", result, want)
		}
	}
	for _, forbidden := range []string{"###", "`", "**", "| --- |"} {
		if strings.Contains(result, forbidden) {
			t.Fatalf("clean result = %q, should not contain %q", result, forbidden)
		}
	}
}

func TestStreamFrameEventsSuppressEmbeddedParentToolEcho(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		SessionRef: session.SessionRef{SessionID: "root-session"},
		CallID:     "spawn-call-1",
		ToolName:   "SPAWN",
		RawInput:   map[string]any{"agent": "self", "prompt": "inspect"},
		Ref:        stream.Ref{SessionID: "root-session", TaskID: "jack"},
		Scope:      EventScopeMain,
	}
	events := StreamFrameEvents(req, stream.Frame{
		Ref:     req.Ref,
		Running: true,
		Event: &session.Event{
			Type:       session.EventTypeToolCall,
			Visibility: session.VisibilityCanonical,
			Scope: &session.EventScope{
				Participant: session.ParticipantRef{
					ID:           "self-1",
					Kind:         session.ParticipantKindSubagent,
					Role:         session.ParticipantRoleDelegated,
					DelegationID: "jack",
				},
				ACP: session.ACPRef{SessionID: "child-session"},
			},
			Protocol: &session.EventProtocol{
				Method:     session.ProtocolMethodSessionUpdate,
				UpdateType: string(session.ProtocolUpdateTypeToolCall),
				Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeToolCall),
					ToolCallID:    "spawn-call-1",
					Kind:          "SPAWN",
					Title:         `SPAWN {"agent":"self","prompt":"inspect"}`,
					Status:        "running",
					RawInput:      map[string]any{"agent": "self", "prompt": "inspect"},
				},
			},
		},
	})
	if len(events) != 0 {
		t.Fatalf("StreamFrameEvents() = %#v, want parent SPAWN tool echo suppressed", events)
	}
}

func sessionTextContent(t *testing.T, content []session.ProtocolToolCallContent) string {
	t.Helper()
	if len(content) != 1 {
		t.Fatalf("content = %#v, want one item", content)
	}
	payload, _ := content[0].Content.(map[string]any)
	text, _ := payload["text"].(string)
	return text
}
