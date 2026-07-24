package projector

import (
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestProjectApprovalPayloadEnvelopeUsesPermissionProjectorPolicy(t *testing.T) {
	t.Parallel()

	events := ProjectApprovalPayloadEnvelope(eventstream.Envelope{
		SessionID: "session-1",
		Meta: map[string]any{
			"request_id": "approval-1",
		},
	}, &approval.Payload{
		ToolCallID: "call-1",
		ToolName:   "RUN_COMMAND",
		RawInput:   map[string]any{"command": "go test ./..."},
		RawOutput:  map[string]any{"preview": "would run tests"},
		Content: []session.ProtocolToolCallContent{{
			Type:    "content",
			Content: session.ProtocolTextContent("permission detail"),
		}},
		Reason:             "needs execution",
		Justification:      "requested by user",
		SandboxPermissions: "workspace-write",
		Status:             approval.StatusPending,
		Options: []approval.Option{{
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
	if stringPtrValue(permission.ToolCall.Title) != "RunCommand go test ./..." {
		t.Fatalf("permission tool title = %q, want summarized title", stringPtrValue(permission.ToolCall.Title))
	}
	rawInput, ok := permission.ToolCall.RawInput.(map[string]any)
	if !ok {
		t.Fatalf("permission raw input = %#v, want map", permission.ToolCall.RawInput)
	}
	if rawInput["approval_reason"] != "needs execution" || rawInput["justification"] != "requested by user" || rawInput["sandbox_permissions"] != "workspace-write" {
		t.Fatalf("permission raw input = %#v, want approval prompt fields", rawInput)
	}
	if rawOutput, ok := permission.ToolCall.RawOutput.(map[string]any); !ok || rawOutput["preview"] != "would run tests" {
		t.Fatalf("permission raw output = %#v, want preserved preview", permission.ToolCall.RawOutput)
	}
	if len(permission.ToolCall.Content) != 1 || permission.ToolCall.Content[0].Type != "content" {
		t.Fatalf("permission content = %#v, want preserved canonical content", permission.ToolCall.Content)
	}
	payload := ApprovalPayloadFromPermission(permission)
	if payload == nil || payload.ToolName != "RUN_COMMAND" || len(payload.Content) != 1 {
		t.Fatalf("approval payload = %#v, want canonical RUN_COMMAND tool name", payload)
	}
}

func TestProjectTaskStreamFrameDoesNotProjectChildPermissionOutsideControl(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		HandleID:   "handle-1",
		RunID:      "run-1",
		TurnID:     "turn-1",
		SessionRef: session.SessionRef{SessionID: "root-session"},
		CallID:     "spawn-call-1",
		ToolName:   "SPAWN",
		RawInput:   map[string]any{"agent": "helper", "prompt": "inspect"},
		Ref:        stream.Ref{SessionID: "root-session", TaskID: "task-1", TerminalID: "child-terminal-1"},
		Scope:      eventstream.ScopeMain,
	}
	frame := stream.Frame{
		Ref:       req.Ref,
		Running:   true,
		State:     "waiting_approval",
		UpdatedAt: time.Unix(200, 0),
		Event: &session.Event{
			ID:         "approval-child-1",
			Type:       session.EventTypeLifecycle,
			Visibility: session.VisibilityUIOnly,
			Scope: &session.EventScope{
				Participant: session.ParticipantRef{
					ID:           "helper-1",
					Kind:         session.ParticipantKindSubagent,
					Role:         session.ParticipantRoleDelegated,
					DelegationID: "task-1",
				},
				ACP: session.ACPRef{SessionID: "child-session"},
			},
			Protocol: &session.EventProtocol{
				Method: session.ProtocolMethodRequestPermission,
				Permission: &session.ProtocolApproval{
					ToolCall: session.ProtocolToolCall{
						ID:        "shared-call",
						Name:      "WRITE",
						Kind:      "edit",
						Title:     "Write file",
						Status:    "pending",
						RawInput:  map[string]any{"path": "child.txt"},
						RawOutput: map[string]any{"preview": "new text"},
						Content: []session.ProtocolToolCallContent{{
							Type:    "content",
							Content: session.ProtocolTextContent("child permission detail"),
						}},
					},
					Options: []session.ProtocolApprovalOption{{
						ID: "allow_once", Name: "Allow once", Kind: "allow_once",
					}},
				},
			},
		},
	}

	events := ProjectTaskStreamFrame(req, frame)
	if len(events) != 0 {
		t.Fatalf("ProjectTaskStreamFrame() = %#v, want child permission withheld for Control routing", events)
	}
}

func TestProjectTaskStreamFrameBuildsStandardToolUpdateEnvelope(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		HandleID:   "handle-1",
		RunID:      "run-1",
		TurnID:     "turn-1",
		SessionRef: session.SessionRef{SessionID: "session-1"},
		CallID:     "call-1",
		ToolName:   "RUN_COMMAND",
		TaskHandle: "command",
		RawInput:   map[string]any{"command": "echo ok"},
		Ref: stream.Ref{
			SessionID:  "session-1",
			TaskID:     "task-1",
			TerminalID: "internal-terminal-1",
		},
		DisplayTerminalID: "call-1",
		Scope:             eventstream.ScopeMain,
	}

	events := ProjectTaskStreamFrame(req, stream.Frame{
		Ref:             req.Ref,
		Text:            "ok\n",
		Cursor:          stream.Cursor{Output: 15},
		TruncatedBefore: 12,
		Running:         true,
		UpdatedAt:       time.Unix(100, 0),
	})
	if len(events) != 1 {
		t.Fatalf("ProjectTaskStreamFrame() returned %d events: %#v", len(events), events)
	}
	env := events[0]
	if env.Kind != eventstream.KindSessionUpdate || env.SessionID != "session-1" || env.HandleID != "handle-1" {
		t.Fatalf("env = %#v, want session/update with transport ids", env)
	}
	update, ok := env.Update.(schema.ToolCallUpdate)
	if !ok {
		t.Fatalf("env.Update = %#v, want ToolCallUpdate", env.Update)
	}
	if update.ToolCallID != "call-1" {
		t.Fatalf("tool update = %#v, want call-1", update)
	}
	if update.Kind != nil || update.Title != nil || update.RawInput != nil {
		t.Fatalf("tool update = %#v, stream append should not repeat stable tool fields", update)
	}
	if got := stringPtrValue(update.Status); got != schema.ToolStatusInProgress {
		t.Fatalf("status = %q, want in_progress for terminal append", got)
	}
	assertTerminalAnchor(t, update.Content, "call-1")
	if got := toolTerminalOutputText(t, update); got != "ok\n" {
		t.Fatalf("terminal output = %q, want ok output", got)
	}
	if metautil.Bool(update.Meta, metautil.Root, metautil.Transient) {
		t.Fatalf("update.Meta = %#v, want typed delivery without legacy transient shadow", update.Meta)
	}
	streamMeta := metautil.RuntimeSection(update.Meta, metautil.RuntimeStream)
	if streamMeta[metautil.RuntimeStreamTruncated] != true || streamMeta[metautil.RuntimeStreamBefore] != int64(12) {
		t.Fatalf("stream meta = %#v, want typed truncation boundary", streamMeta)
	}
	if got, ok := metautil.Int64(update.Meta, metautil.Root, metautil.Runtime, metautil.RuntimeStream, metautil.RuntimeOutputCursor); !ok || got != 15 {
		t.Fatalf("stream output cursor = %d, %v; want 15, true", got, ok)
	}
	toolMeta := metautil.RuntimeSection(update.Meta, metautil.RuntimeTool)
	if toolMeta[metautil.RuntimeTargetHandle] != "command" {
		t.Fatalf("tool meta = %#v, want public Task handle", toolMeta)
	}
	if _, leaked := toolMeta[metautil.RuntimeTargetID]; leaked {
		t.Fatalf("tool meta = %#v, opaque TaskID leaked as display identity", toolMeta)
	}
	assertStreamDelivery(t, env, true)
}

func TestProjectTaskStreamFrameKeepsOneEnvelopeWhenNarrativeCarriesUsage(t *testing.T) {
	t.Parallel()

	req := spawnStreamRequestForTest()
	event := childMessageEventForStreamTest("child answer")
	event.Meta = map[string]any{"usage": map[string]any{
		"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5,
	}}
	events := ProjectTaskStreamFrame(req, stream.Frame{
		Ref:     req.Ref,
		Cursor:  stream.Cursor{Events: 1},
		Running: true,
		Event:   event,
	})
	if len(events) != 1 {
		t.Fatalf("ProjectTaskStreamFrame() returned %d envelopes, want one cursor-resumable unit: %#v", len(events), events)
	}
	if eventstream.UpdateType(events[0].Update) == schema.UpdateUsage {
		t.Fatalf("Task frame projected only sibling usage and lost its narrative: %#v", events[0])
	}
}

func TestProjectTaskStreamFramePreservesClosedCommandExitCode(t *testing.T) {
	t.Parallel()

	exitCode := 7
	req := StreamRequest{
		SessionRef:        session.SessionRef{SessionID: "session-1"},
		CallID:            "call-1",
		ToolName:          "RUN_COMMAND",
		RawInput:          map[string]any{"command": "false"},
		Ref:               stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "term-1"},
		DisplayTerminalID: "call-1",
		Scope:             eventstream.ScopeMain,
	}
	events := ProjectTaskStreamFrame(req, stream.Frame{
		Ref:      req.Ref,
		Cursor:   stream.Cursor{Output: 3},
		Closed:   true,
		State:    "failed",
		ExitCode: &exitCode,
	})
	if len(events) != 1 {
		t.Fatalf("ProjectTaskStreamFrame() returned %d events: %#v", len(events), events)
	}
	update := requireToolUpdate(t, events[0])
	if got := stringPtrValue(update.Status); got != schema.ToolStatusFailed {
		t.Fatalf("final status = %q, want failed", got)
	}
	exit, ok := metautil.TerminalExit(update.Meta)
	if !ok || exit.TerminalID != "call-1" || exit.ExitCode == nil || *exit.ExitCode != exitCode {
		t.Fatalf("terminal exit = %#v, %v; want exit code %d", exit, ok, exitCode)
	}
	assertTerminalAnchor(t, update.Content, "call-1")
	if got, ok := metautil.Int64(update.Meta, metautil.Root, metautil.Runtime, metautil.RuntimeStream, metautil.RuntimeOutputCursor); !ok || got != 3 {
		t.Fatalf("final stream output cursor = %d, %v; want 3, true", got, ok)
	}
}

func TestProjectTaskStreamFramePreservesSplitNewlineFrame(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		SessionRef: session.SessionRef{SessionID: "session-1"},
		CallID:     "call-1",
		ToolName:   "RUN_COMMAND",
		RawInput:   map[string]any{"command": "echo lines"},
		Ref:        stream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "terminal-1"},
		Scope:      eventstream.ScopeMain,
	}
	var projected strings.Builder
	for _, frame := range []stream.Frame{
		{Ref: req.Ref, Text: "Step 1/2", Cursor: stream.Cursor{Output: 8}, Running: true},
		{Ref: req.Ref, Text: "\n", Cursor: stream.Cursor{Output: 9}, Running: true},
		{Ref: req.Ref, Text: "Step 2/2\n", Cursor: stream.Cursor{Output: 18}, Running: true},
	} {
		events := ProjectTaskStreamFrame(req, frame)
		if len(events) != 1 {
			t.Fatalf("ProjectTaskStreamFrame(%q) returned %d events: %#v", frame.Text, len(events), events)
		}
		update, ok := events[0].Update.(schema.ToolCallUpdate)
		if !ok {
			t.Fatalf("Update = %T, want ToolCallUpdate", events[0].Update)
		}
		projected.WriteString(toolTerminalOutputText(t, update))
	}
	if got, want := projected.String(), "Step 1/2\nStep 2/2\n"; got != want {
		t.Fatalf("projected terminal output = %q, want %q", got, want)
	}
}

func TestProjectTaskStreamFrameFinalDoesNotRepeatStreamedOutput(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		SessionRef: session.SessionRef{SessionID: "root-session"},
		CallID:     "command-1",
		ToolName:   "RUN_COMMAND",
		RawInput:   map[string]any{"command": "printf ok"},
		Ref:        stream.Ref{SessionID: "root-session", TaskID: "task-1", TerminalID: "terminal-1"},
		Scope:      eventstream.ScopeMain,
	}
	events := ProjectTaskStreamFrame(req, stream.Frame{
		Ref:     req.Ref,
		Text:    "ok\n",
		Cursor:  stream.Cursor{Output: 3},
		Closed:  true,
		Running: false,
		State:   "completed",
	})
	if len(events) != 1 {
		t.Fatalf("ProjectTaskStreamFrame(RUN_COMMAND closed) returned %d events: %#v", len(events), events)
	}
	update := requireToolUpdate(t, events[0])
	if got := stringPtrValue(update.Status); got != schema.ToolStatusCompleted {
		t.Fatalf("status = %q, want completed", got)
	}
	assertTerminalAnchor(t, update.Content, "command-1")
}

func TestProjectTaskStreamFrameProjectsDelegatedTaskSemanticsWithoutParentText(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		SessionRef:        session.SessionRef{SessionID: "root-session"},
		CallID:            "task-call-1",
		ToolName:          "TASK",
		RawInput:          map[string]any{"action": "write", "task_id": "jack"},
		Ref:               stream.Ref{SessionID: "root-session", TaskID: "jack", TerminalID: "subagent-jack"},
		DisplayTerminalID: "task-call-1",
		Scope:             eventstream.ScopeMain,
	}
	events := ProjectTaskStreamFrame(req, stream.Frame{
		Ref:       req.Ref,
		Text:      "child output\n",
		Cursor:    stream.Cursor{Output: 13, Events: 1},
		Running:   true,
		UpdatedAt: time.Unix(150, 0),
		Event: &session.Event{
			ID:         "child-event-1",
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical,
			Text:       "child output\n",
			Scope: &session.EventScope{
				Participant: session.ParticipantRef{
					ID:           "agent-1",
					Kind:         session.ParticipantKindSubagent,
					Role:         session.ParticipantRoleDelegated,
					DelegationID: "jack",
				},
				ACP: session.ACPRef{SessionID: "child-session"},
			},
		},
	})
	if len(events) != 1 {
		t.Fatalf("ProjectTaskStreamFrame() returned %d events: %#v, want one embedded child event", len(events), events)
	}
	embedded := events[0]
	if embedded.Scope != eventstream.ScopeSubagent || embedded.ScopeID != "jack" || eventstream.UpdateType(embedded.Update) != schema.UpdateAgentMessage {
		t.Fatalf("embedded event = %#v, want subagent agent message", embedded)
	}
	if embedded.ParentTool == nil || embedded.ParentTool.ToolCallID != "task-call-1" || embedded.ParentTool.ToolName != "TASK" {
		t.Fatalf("embedded parent relation = %#v, want TASK/task-call-1", embedded.ParentTool)
	}
	assertStreamDelivery(t, embedded, true)
	assertNoLegacyRelationDeliveryMetadata(t, embedded)
	if _, ok := streamEnvelopeTerminalOutput(embedded); ok {
		t.Fatalf("child semantic envelope = %#v, must not carry parent terminal text", embedded)
	}
}

func TestProjectTaskStreamFrameKeepsNoOutputPlaceholderOutOfTerminalBytes(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		SessionRef: session.SessionRef{SessionID: "root-session"},
		CallID:     "command-1",
		ToolName:   "RUN_COMMAND",
		RawInput:   map[string]any{"command": "false"},
		Ref:        stream.Ref{SessionID: "root-session", TaskID: "task-1", TerminalID: "terminal-1"},
		Scope:      eventstream.ScopeMain,
	}
	events := ProjectTaskStreamFrame(req, stream.Frame{
		Ref:     req.Ref,
		Closed:  true,
		Running: false,
		State:   "failed",
	})
	if len(events) != 1 {
		t.Fatalf("ProjectTaskStreamFrame(RUN_COMMAND closed) returned %d events: %#v", len(events), events)
	}
	update := requireToolUpdate(t, events[0])
	if stringPtrValue(update.Status) != schema.ToolStatusFailed {
		t.Fatalf("update = %+v, want failed RUN_COMMAND result", update)
	}
	if output, ok := metautil.TerminalOutput(update.Meta); ok {
		t.Fatalf("terminal output = %#v, want no synthetic bytes for silent failure", output)
	}
}

func TestProjectTaskStreamFrameProjectsSubagentSemanticEventWithoutParentTerminal(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		HandleID:          "handle-1",
		RunID:             "run-1",
		TurnID:            "turn-1",
		SessionRef:        session.SessionRef{SessionID: "root-session"},
		CallID:            "spawn-call-1",
		ToolName:          "SPAWN",
		RawInput:          map[string]any{"agent": "self", "prompt": "inspect"},
		Ref:               stream.Ref{SessionID: "root-session", TaskID: "jack", TerminalID: "subagent-jack"},
		DisplayTerminalID: "spawn-call-1",
		Origin:            &StreamOrigin{Scope: eventstream.ScopeMain, ScopeID: "root-session", Actor: "assistant"},
		Scope:             eventstream.ScopeMain,
	}
	frame := stream.Frame{
		Ref:       stream.Ref{SessionID: "root-session", TaskID: "jack", TerminalID: "subagent-jack-turn-1"},
		Text:      "The user wants a file",
		Cursor:    stream.Cursor{Output: 21, Events: 1},
		Running:   true,
		UpdatedAt: time.Unix(200, 0),
		Event: &session.Event{
			ID:         "child-event-1",
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityUIOnly,
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
			Protocol: &session.EventProtocol{
				Method: session.ProtocolMethodSessionUpdate,
				Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
					MessageID:     "child-message-1",
					Content:       session.ProtocolTextContent("The user wants a file"),
				},
			},
		},
	}

	events := ProjectTaskStreamFrame(req, frame)
	if len(events) != 1 {
		t.Fatalf("ProjectTaskStreamFrame() returned %d events, want child semantic event only: %#v", len(events), events)
	}
	assertSpawnSemanticEnvelope(t, events[0], "jack", "spawn-call-1")
	message, ok := events[0].Update.(schema.ContentChunk)
	if !ok {
		t.Fatalf("child update = %T, want ContentChunk", events[0].Update)
	}
	content, ok := message.Content.(schema.TextContent)
	if !ok || message.SessionUpdate != schema.UpdateAgentMessage || message.MessageID != "child-message-1" || content.Text != "The user wants a file" {
		t.Fatalf("child message = %#v, want original ACP message chunk fields", message)
	}
	if _, ok := streamEnvelopeTerminalOutput(events[0]); ok {
		t.Fatalf("child semantic envelope = %#v, want no parent terminal output", events[0])
	}
}

func TestProjectTaskStreamFrameProjectsEventOnlySpawnChildSemantics(t *testing.T) {
	t.Parallel()

	oldText := "old line\n"
	req := spawnStreamRequestForTest()
	cases := []struct {
		name   string
		event  *session.Event
		assert func(*testing.T, eventstream.Envelope)
	}{
		{
			name: "agent message",
			event: &session.Event{
				ID:         "child-message-1",
				Type:       session.EventTypeAssistant,
				Visibility: session.VisibilityUIOnly,
				Text:       "child answer",
				Scope:      spawnSubagentScope("jack"),
				Protocol: &session.EventProtocol{
					Method: session.ProtocolMethodSessionUpdate,
					Update: &session.ProtocolUpdate{
						SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
						MessageID:     "child-message-1",
						Content:       session.ProtocolTextContent("child answer"),
					},
				},
			},
			assert: func(t *testing.T, env eventstream.Envelope) {
				t.Helper()
				update, ok := env.Update.(schema.ContentChunk)
				if !ok {
					t.Fatalf("child update = %T, want ContentChunk", env.Update)
				}
				content, ok := update.Content.(schema.TextContent)
				if !ok || update.SessionUpdate != schema.UpdateAgentMessage || update.MessageID != "child-message-1" || content.Text != "child answer" {
					t.Fatalf("child message update = %#v, want original message fields", update)
				}
			},
		},
		{
			name: "tool update with diff",
			event: &session.Event{
				ID:         "child-tool-update-1",
				Type:       session.EventTypeToolResult,
				Visibility: session.VisibilityUIOnly,
				Scope:      spawnSubagentScope("jack"),
				Protocol: &session.EventProtocol{
					Method: session.ProtocolMethodSessionUpdate,
					Update: &session.ProtocolUpdate{
						SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
						ToolCallID:    "child-patch-1",
						Kind:          "PATCH",
						Status:        schema.ToolStatusCompleted,
						Content: []session.ProtocolToolCallContent{{
							Type:    "diff",
							Path:    "/workspace/demo.txt",
							OldText: &oldText,
							NewText: "new line\n",
						}},
					},
				},
			},
			assert: func(t *testing.T, env eventstream.Envelope) {
				t.Helper()
				update := requireToolUpdate(t, env)
				if update.ToolCallID != "child-patch-1" || update.ToolCallID == "spawn-call-1" {
					t.Fatalf("child tool update = %#v, want distinct child tool call id", update)
				}
				if len(update.Content) != 1 {
					t.Fatalf("child tool content = %#v, want one diff", update.Content)
				}
				diff := update.Content[0]
				if diff.Type != "diff" || diff.Path != "/workspace/demo.txt" || diff.OldText == nil || *diff.OldText != oldText || diff.NewText != "new line\n" {
					t.Fatalf("child tool diff = %#v, want original standard diff fields", diff)
				}
			},
		},
		{
			name: "plan",
			event: &session.Event{
				ID:         "child-plan-1",
				Type:       session.EventTypePlan,
				Visibility: session.VisibilityUIOnly,
				Scope:      spawnSubagentScope("jack"),
				Protocol: &session.EventProtocol{
					Method: session.ProtocolMethodSessionUpdate,
					Update: &session.ProtocolUpdate{
						SessionUpdate: string(session.ProtocolUpdateTypePlan),
						Entries: []session.ProtocolPlanEntry{{
							Content:  "inspect stream projection",
							Status:   "in_progress",
							Priority: "high",
						}},
					},
				},
			},
			assert: func(t *testing.T, env eventstream.Envelope) {
				t.Helper()
				update, ok := env.Update.(schema.PlanUpdate)
				if !ok {
					t.Fatalf("child update = %T, want PlanUpdate", env.Update)
				}
				if len(update.Entries) != 1 || update.Entries[0].Content != "inspect stream projection" || update.Entries[0].Status != "in_progress" || update.Entries[0].Priority != "high" {
					t.Fatalf("child plan = %#v, want original plan entry", update)
				}
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			events := ProjectTaskStreamFrame(req, stream.Frame{
				Ref:     req.Ref,
				Running: true,
				Event:   tc.event,
			})
			if len(events) != 1 {
				t.Fatalf("ProjectTaskStreamFrame() returned %d events, want one child semantic event: %#v", len(events), events)
			}
			assertSpawnSemanticEnvelope(t, events[0], "jack", "spawn-call-1")
			tc.assert(t, events[0])
		})
	}
}

func TestProjectTaskStreamFrameDropsDelegatedTextOnlyRunningFrame(t *testing.T) {
	t.Parallel()

	req := spawnStreamRequestForTest()
	events := ProjectTaskStreamFrame(req, stream.Frame{
		Ref:     req.Ref,
		Text:    "child stream text\n",
		Running: true,
	})
	if len(events) != 0 {
		t.Fatalf("ProjectTaskStreamFrame(text-only) = %#v, want no compatibility output", events)
	}

	events = ProjectTaskStreamFrame(req, stream.Frame{Ref: req.Ref, Running: true})
	if len(events) != 0 {
		t.Fatalf("ProjectTaskStreamFrame(empty) = %#v, want no output", events)
	}
}

func TestProjectTaskStreamFrameMarksClosedSpawnEventFinalWithoutParentCopy(t *testing.T) {
	t.Parallel()

	req := spawnStreamRequestForTest()
	events := ProjectTaskStreamFrame(req, stream.Frame{
		Ref:    req.Ref,
		Cursor: stream.Cursor{Output: 19},
		Closed: true,
		State:  "completed",
		Event: &session.Event{
			ID:         "child-message-final",
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityUIOnly,
			Text:       "child final semantic result",
			Scope:      spawnSubagentScope("jack"),
			Protocol: &session.EventProtocol{
				Method: session.ProtocolMethodSessionUpdate,
				Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
					MessageID:     "child-message-final",
					Content:       session.ProtocolTextContent("child final semantic result"),
				},
			},
		},
	})
	if len(events) != 1 {
		t.Fatalf("ProjectTaskStreamFrame() returned %d events, want only the child semantic event: %#v", len(events), events)
	}
	assertSpawnSemanticEnvelope(t, events[0], "jack", "spawn-call-1")
	if !events[0].Final {
		t.Fatalf("closed child semantic envelope = %#v, want final Task-stream frame", events[0])
	}
	if update, ok := events[0].Update.(schema.ContentChunk); !ok || update.MessageID != "child-message-final" {
		t.Fatalf("closed child semantic update = %#v, want final child message", events[0].Update)
	}
}

func TestProjectTaskStreamFrameSubagentFinalStatesAreLifecycleOnly(t *testing.T) {
	t.Parallel()

	cases := []string{"completed", "failed", "cancelled", "interrupted", "terminated", "unknown_outcome"}
	for _, tc := range cases {
		tc := tc
		t.Run(tc, func(t *testing.T) {
			req := spawnStreamRequestForTest()
			events := ProjectTaskStreamFrame(req, stream.Frame{
				Ref:    req.Ref,
				Text:   "### accumulated child output\n",
				Cursor: stream.Cursor{Output: 27},
				Closed: true,
				State:  tc,
			})
			if len(events) != 1 {
				t.Fatalf("ProjectTaskStreamFrame(%s) returned %d events, want one Task lifecycle: %#v", tc, len(events), events)
			}
			if events[0].Kind != eventstream.KindLifecycle || events[0].Lifecycle == nil || events[0].Lifecycle.State != tc || !events[0].Final {
				t.Fatalf("Task terminal = %#v, want final lifecycle %q", events[0], tc)
			}
			if events[0].Update != nil {
				t.Fatalf("Task terminal update = %#v, must not manufacture a parent tool result", events[0].Update)
			}
			if events[0].ParentTool == nil || events[0].ParentTool.ToolCallID != "spawn-call-1" {
				t.Fatalf("Task terminal parent = %#v, want canonical Spawn relation", events[0].ParentTool)
			}
		})
	}
}

func TestProjectTaskStreamFrameDoesNotPromoteDelegatedResultIntoParentTool(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		SessionRef:        session.SessionRef{SessionID: "root-session"},
		CallID:            "spawn-call-1",
		ToolName:          "SPAWN",
		RawInput:          map[string]any{"agent": "self", "prompt": "inspect"},
		Ref:               stream.Ref{SessionID: "root-session", TaskID: "jack", TerminalID: "subagent-jack"},
		DisplayTerminalID: "spawn-call-1",
		Scope:             eventstream.ScopeMain,
	}
	events := ProjectTaskStreamFrame(req, stream.Frame{
		Ref:     stream.Ref{SessionID: "root-session", TaskID: "jack", TerminalID: "subagent-jack-turn-1"},
		Text:    "Final child result\n",
		Closed:  true,
		Running: false,
		State:   "completed",
	})
	if len(events) != 1 {
		t.Fatalf("ProjectTaskStreamFrame() returned %d events, want Task lifecycle: %#v", len(events), events)
	}
	if events[0].Kind != eventstream.KindLifecycle || events[0].Lifecycle == nil || events[0].Lifecycle.State != eventstream.LifecycleStateCompleted || events[0].Update != nil {
		t.Fatalf("Task terminal = %#v, want lifecycle-only completion", events[0])
	}
}

func TestProjectTaskStreamFrameSuppressesEmbeddedParentToolEcho(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		SessionRef: session.SessionRef{SessionID: "root-session"},
		CallID:     "spawn-call-1",
		ToolName:   "SPAWN",
		RawInput:   map[string]any{"agent": "self", "prompt": "inspect"},
		Ref:        stream.Ref{SessionID: "root-session", TaskID: "jack"},
		Scope:      eventstream.ScopeMain,
	}
	events := ProjectTaskStreamFrame(req, stream.Frame{
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
		t.Fatalf("ProjectTaskStreamFrame() = %#v, want parent SPAWN tool echo suppressed", events)
	}
}

type streamFrameEnvelopeExpectation struct {
	parentCallID      string
	parentTool        string
	transient         bool
	terminalOutput    string
	hasTerminalOutput bool
}

func assertStreamFrameEnvelopeExpectation(t *testing.T, env eventstream.Envelope, want streamFrameEnvelopeExpectation) {
	t.Helper()
	if want.parentCallID == "" {
		if env.ParentTool != nil {
			t.Fatalf("parent relation = %#v, want none", env.ParentTool)
		}
	} else if env.ParentTool == nil || env.ParentTool.ToolCallID != want.parentCallID || env.ParentTool.ToolName != want.parentTool {
		t.Fatalf("parent relation = %#v, want %s/%s", env.ParentTool, want.parentTool, want.parentCallID)
	}
	assertStreamDelivery(t, env, want.transient)
	gotTerminalOutput, hasTerminalOutput := streamEnvelopeTerminalOutput(env)
	if hasTerminalOutput != want.hasTerminalOutput || gotTerminalOutput != want.terminalOutput {
		t.Fatalf("terminal output = %q/%t, want %q/%t; env=%#v", gotTerminalOutput, hasTerminalOutput, want.terminalOutput, want.hasTerminalOutput, env)
	}
}

func streamEnvelopeTerminalOutput(env eventstream.Envelope) (string, bool) {
	update, ok := eventstream.ToolCallUpdateFromEnvelope(env)
	if !ok {
		return "", false
	}
	output, ok := metautil.TerminalOutput(update.Meta)
	if !ok {
		return "", false
	}
	return output.Data, true
}

func childMessageEventForStreamTest(text string) *session.Event {
	return &session.Event{
		ID:         "child-message-stream-1",
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityUIOnly,
		Text:       text,
		Scope:      spawnSubagentScope("jack"),
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodSessionUpdate,
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
				MessageID:     "child-message-stream-1",
				Content:       session.ProtocolTextContent(text),
			},
		},
	}
}

func childPlanEventForStreamTest() *session.Event {
	return &session.Event{
		ID:         "child-plan-stream-1",
		Type:       session.EventTypePlan,
		Visibility: session.VisibilityUIOnly,
		Scope:      spawnSubagentScope("jack"),
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodSessionUpdate,
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypePlan),
				Entries: []session.ProtocolPlanEntry{{
					Content: "inspect parent delivery",
					Status:  "in_progress",
				}},
			},
		},
	}
}

func childToolUpdateEventForStreamTest() *session.Event {
	return &session.Event{
		ID:         "child-tool-stream-1",
		Type:       session.EventTypeToolResult,
		Visibility: session.VisibilityUIOnly,
		Scope:      spawnSubagentScope("jack"),
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodSessionUpdate,
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
				ToolCallID:    "child-tool-stream-1",
				Kind:          "PATCH",
				Status:        schema.ToolStatusCompleted,
			},
		},
	}
}

func spawnStreamRequestForTest() StreamRequest {
	return StreamRequest{
		HandleID:          "handle-1",
		RunID:             "run-1",
		TurnID:            "turn-1",
		SessionRef:        session.SessionRef{SessionID: "root-session"},
		CallID:            "spawn-call-1",
		ToolName:          "SPAWN",
		RawInput:          map[string]any{"agent": "self", "prompt": "inspect"},
		Ref:               stream.Ref{SessionID: "root-session", TaskID: "jack", TerminalID: "subagent-jack"},
		DisplayTerminalID: "spawn-call-1",
		Scope:             eventstream.ScopeMain,
	}
}

func spawnSubagentScope(taskID string) *session.EventScope {
	return &session.EventScope{
		Participant: session.ParticipantRef{
			ID:           "self-1",
			Kind:         session.ParticipantKindSubagent,
			Role:         session.ParticipantRoleDelegated,
			DelegationID: taskID,
		},
		ACP: session.ACPRef{SessionID: "child-session"},
	}
}

func assertSpawnSemanticEnvelope(t *testing.T, env eventstream.Envelope, taskID string, parentCallID string) {
	t.Helper()
	if env.Kind != eventstream.KindSessionUpdate || env.Scope != eventstream.ScopeSubagent || env.ScopeID != taskID {
		t.Fatalf("child envelope = %#v, want scoped subagent session/update for task %q", env, taskID)
	}
	if env.ParentTool == nil || env.ParentTool.ToolCallID != parentCallID || env.ParentTool.ToolName != "SPAWN" {
		t.Fatalf("child parent relation = %#v, want SPAWN/%q", env.ParentTool, parentCallID)
	}
	assertStreamDelivery(t, env, true)
	assertNoLegacyRelationDeliveryMetadata(t, env)
}

func assertNoLegacyRelationDeliveryMetadata(t *testing.T, env eventstream.Envelope) {
	t.Helper()
	if parentCallID := metautil.String(env.Meta, metautil.Root, metautil.Runtime, metautil.RuntimeStream, metautil.RuntimeStreamParentCallID); parentCallID != "" {
		t.Fatalf("envelope meta parent_call_id = %q, want typed-only relation; meta=%#v", parentCallID, env.Meta)
	}
	if parentTool := metautil.String(env.Meta, metautil.Root, metautil.Runtime, metautil.RuntimeStream, metautil.RuntimeStreamParentTool); parentTool != "" {
		t.Fatalf("envelope meta parent_tool = %q, want typed-only relation; meta=%#v", parentTool, env.Meta)
	}
	if metautil.Bool(env.Meta, metautil.Root, metautil.Transient) {
		t.Fatalf("envelope meta = %#v, want no legacy delivery shadow", env.Meta)
	}
}

func assertStreamDelivery(t *testing.T, env eventstream.Envelope, transient bool) {
	t.Helper()
	if env.Delivery == nil {
		t.Fatalf("envelope delivery = nil, want transient=%t", transient)
	}
	if (env.Delivery.Mode == eventstream.DeliveryTransient) != transient {
		t.Fatalf("envelope delivery = %#v, want transient=%t", env.Delivery, transient)
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

func toolTerminalOutputText(t *testing.T, update schema.ToolCallUpdate) string {
	t.Helper()
	output, ok := metautil.TerminalOutput(update.Meta)
	if !ok {
		t.Fatalf("meta = %#v, want terminal_output", update.Meta)
	}
	return output.Data
}

func assertStreamTerminalInfo(t *testing.T, meta map[string]any, terminalID string) {
	t.Helper()
	info, ok := metautil.TerminalInfo(meta)
	if !ok || info.TerminalID != terminalID {
		t.Fatalf("terminal_info = %#v, want %q", meta, terminalID)
	}
}

func runtimeTaskMeta(meta map[string]any) map[string]any {
	caelis, _ := meta[metautil.Root].(map[string]any)
	runtimeMeta, _ := caelis[metautil.Runtime].(map[string]any)
	taskMeta, _ := runtimeMeta[metautil.RuntimeTask].(map[string]any)
	return taskMeta
}
