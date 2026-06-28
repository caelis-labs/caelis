package projector

import (
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestProjectApprovalPayloadEnvelopeUsesPermissionProjectorPolicy(t *testing.T) {
	t.Parallel()

	events := ProjectApprovalPayloadEnvelope(eventstream.Envelope{
		SessionID: "session-1",
		Meta: map[string]any{
			"request_id": "approval-1",
		},
	}, &gateway.ApprovalPayload{
		ToolCallID:         "call-1",
		ToolName:           "RUN_COMMAND",
		RawInput:           map[string]any{"command": "go test ./..."},
		Reason:             "needs execution",
		Justification:      "requested by user",
		SandboxPermissions: "workspace-write",
		Status:             gateway.ApprovalStatusPending,
		Options: []gateway.ApprovalOption{{
			ID:   "allow_once",
			Name: "Allow once",
			Kind: "allow_once",
		}},
	})

	if len(events) != 1 || events[0].Kind != eventstream.KindRequestPermission || events[0].Permission == nil {
		t.Fatalf("ProjectApprovalPayloadEnvelope() = %#v, want request_permission", events)
	}
	permission := events[0].Permission
	if permission.SessionID != "session-1" {
		t.Fatalf("permission.SessionID = %q, want session-1", permission.SessionID)
	}
	if stringPtrValue(permission.ToolCall.Kind) != schema.ToolKindExecute {
		t.Fatalf("permission tool kind = %q, want displaypolicy execute kind", stringPtrValue(permission.ToolCall.Kind))
	}
	if stringPtrValue(permission.ToolCall.Title) != "RUN_COMMAND go test ./..." {
		t.Fatalf("permission tool title = %q, want summarized title", stringPtrValue(permission.ToolCall.Title))
	}
	rawInput, ok := permission.ToolCall.RawInput.(map[string]any)
	if !ok {
		t.Fatalf("permission raw input = %#v, want map", permission.ToolCall.RawInput)
	}
	if rawInput["approval_reason"] != "needs execution" || rawInput["justification"] != "requested by user" || rawInput["sandbox_permissions"] != "workspace-write" {
		t.Fatalf("permission raw input = %#v, want approval prompt fields", rawInput)
	}
	payload := ApprovalPayloadFromPermission(permission)
	if payload == nil || payload.ToolName != "RUN_COMMAND" {
		t.Fatalf("approval payload = %#v, want canonical RUN_COMMAND tool name", payload)
	}
}

func TestProjectStreamFrameBuildsStandardToolUpdateEnvelope(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		HandleID:   "handle-1",
		RunID:      "run-1",
		TurnID:     "turn-1",
		SessionRef: session.SessionRef{SessionID: "session-1"},
		CallID:     "call-1",
		ToolName:   "RUN_COMMAND",
		RawInput:   map[string]any{"command": "echo ok"},
		Ref: stream.Ref{
			SessionID:  "session-1",
			TaskID:     "task-1",
			TerminalID: "terminal-1",
		},
		Scope: gateway.EventScopeMain,
	}

	events := ProjectStreamFrame(req, stream.Frame{
		Ref:       req.Ref,
		Text:      "ok\n",
		Cursor:    stream.Cursor{Output: 3},
		Running:   true,
		UpdatedAt: time.Unix(100, 0),
	})
	if len(events) != 1 {
		t.Fatalf("ProjectStreamFrame() returned %d events: %#v", len(events), events)
	}
	env := events[0]
	if env.Kind != eventstream.KindSessionUpdate || env.SessionID != "session-1" || env.HandleID != "handle-1" {
		t.Fatalf("env = %#v, want session/update with transport ids", env)
	}
	update, ok := env.Update.(schema.ToolCallUpdate)
	if !ok {
		t.Fatalf("env.Update = %#v, want ToolCallUpdate", env.Update)
	}
	if update.ToolCallID != "call-1" || stringPtrValue(update.Kind) != "RUN_COMMAND" || stringPtrValue(update.Status) != string(gateway.ToolStatusRunning) {
		t.Fatalf("tool update = %#v, want running RUN_COMMAND call-1", update)
	}
	if len(update.Content) != 1 || update.Content[0].Type != "terminal" || update.Content[0].TerminalID != "terminal-1" || schema.ExtractTextValue(update.Content[0].Content) != "ok\n" {
		t.Fatalf("content = %#v, want terminal output content", update.Content)
	}
	if !gateway.EventMetaBool(update.Meta, gateway.EventMetaRoot, gateway.EventMetaTransient) {
		t.Fatalf("update.Meta = %#v, want transient stream update", update.Meta)
	}
}

func TestProjectStreamFrameDoesNotAppendReasoningTextToParentTool(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		SessionRef: session.SessionRef{SessionID: "root-session"},
		CallID:     "spawn-1",
		ToolName:   "SPAWN",
		RawInput:   map[string]any{"agent": "self", "prompt": "demo"},
		Ref:        stream.Ref{SessionID: "root-session", TaskID: "amy"},
		Scope:      gateway.EventScopeMain,
	}
	events := ProjectStreamFrame(req, stream.Frame{
		Ref:     req.Ref,
		Text:    "The user wants me to inspect files.",
		Running: true,
		Event: &session.Event{
			Type: session.EventTypeAssistant,
			Protocol: &session.EventProtocol{
				Method: session.ProtocolMethodSessionUpdate,
				Update: &session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeAgentThought)},
			},
		},
	})
	for _, env := range events {
		if eventstream.UpdateType(env.Update) == schema.UpdateToolCallInfo {
			t.Fatalf("reasoning frame events = %#v, should not append parent tool update", events)
		}
	}

	events = ProjectStreamFrame(req, stream.Frame{
		Ref:     req.Ref,
		Text:    "final visible output",
		Running: true,
	})
	if len(events) != 1 {
		t.Fatalf("stdout frame events = %#v, want one parent tool update", events)
	}
	update := requireToolUpdate(t, events[0])
	if update.ToolCallID != "spawn-1" {
		t.Fatalf("tool update = %#v, want parent spawn call", update)
	}
}

func TestProjectStreamFrameUsesNoOutputPlaceholderForSilentCommandFailure(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		SessionRef: session.SessionRef{SessionID: "root-session"},
		CallID:     "command-1",
		ToolName:   "RUN_COMMAND",
		RawInput:   map[string]any{"command": "false"},
		Ref:        stream.Ref{SessionID: "root-session", TaskID: "task-1", TerminalID: "terminal-1"},
		Scope:      gateway.EventScopeMain,
	}
	events := ProjectStreamFrame(req, stream.Frame{
		Ref:     req.Ref,
		Closed:  true,
		Running: false,
		State:   "failed",
	})
	if len(events) != 1 {
		t.Fatalf("ProjectStreamFrame(RUN_COMMAND closed) returned %d events: %#v", len(events), events)
	}
	update := requireToolUpdate(t, events[0])
	if stringPtrValue(update.Status) != string(gateway.ToolStatusFailed) {
		t.Fatalf("update = %+v, want failed RUN_COMMAND result", update)
	}
	if strings.Contains(toolContentText(t, update.Content), "exit 1") {
		t.Fatalf("content = %#v, should not expose exit code as terminal output", update.Content)
	}
	if got := toolContentText(t, update.Content); got != "(no output)" {
		t.Fatalf("content text = %q, want no-output placeholder", got)
	}
}

func TestProjectStreamFramePreservesEmbeddedSubagentEventAndToolUpdate(t *testing.T) {
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
		Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session", Actor: "assistant"},
		Scope:      gateway.EventScopeMain,
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

	events := ProjectStreamFrame(req, frame)
	if len(events) != 2 {
		t.Fatalf("ProjectStreamFrame() returned %d events, want embedded child event and tool update: %#v", len(events), events)
	}
	child := events[0]
	if child.Kind != eventstream.KindSessionUpdate || child.Scope != eventstream.ScopeSubagent || child.ScopeID != "jack" {
		t.Fatalf("child envelope = %#v, want subagent session/update keyed by SPAWN task", child)
	}
	chunk, ok := child.Update.(schema.ContentChunk)
	if !ok || schema.ExtractTextValue(chunk.Content) != "The user wants a file" {
		t.Fatalf("child update = %#v, want assistant content chunk", child.Update)
	}
	if !gateway.EventMetaBool(child.Meta, gateway.EventMetaRoot, gateway.EventMetaRuntime, gateway.EventMetaRuntimeStream, gateway.EventMetaRuntimeStreamMirroredToParentTool) {
		t.Fatalf("child meta = %#v, want mirrored_to_parent_tool marker when parent SPAWN update is also projected", child.Meta)
	}
	tool := requireToolUpdate(t, events[1])
	if got := toolContentText(t, tool.Content); got != "The user wants a file" {
		t.Fatalf("tool content = %q, want original stream text", got)
	}
}

func TestProjectStreamFrameSuppressesEmbeddedParentToolEcho(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		SessionRef: session.SessionRef{SessionID: "root-session"},
		CallID:     "spawn-call-1",
		ToolName:   "SPAWN",
		RawInput:   map[string]any{"agent": "self", "prompt": "inspect"},
		Ref:        stream.Ref{SessionID: "root-session", TaskID: "jack"},
		Scope:      gateway.EventScopeMain,
	}
	events := ProjectStreamFrame(req, stream.Frame{
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
				Method: session.ProtocolMethodSessionUpdate,
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
		t.Fatalf("ProjectStreamFrame() = %#v, want parent SPAWN tool echo suppressed", events)
	}
}

func requireToolUpdate(t *testing.T, env eventstream.Envelope) schema.ToolCallUpdate {
	t.Helper()
	update, ok := eventstream.ToolCallUpdateFromEnvelope(env)
	if !ok {
		t.Fatalf("env = %#v, want ToolCallUpdate", env)
	}
	return update
}

func toolContentText(t *testing.T, content []schema.ToolCallContent) string {
	t.Helper()
	if len(content) != 1 {
		t.Fatalf("content = %#v, want one item", content)
	}
	return schema.ExtractTextValue(content[0].Content)
}
