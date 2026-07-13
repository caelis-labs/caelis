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

func TestProjectStreamFrameDoesNotProjectChildPermissionOutsideControl(t *testing.T) {
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

	events := ProjectStreamFrame(req, frame)
	if len(events) != 0 {
		t.Fatalf("ProjectStreamFrame() = %#v, want child permission withheld for Control routing", events)
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
			TerminalID: "internal-terminal-1",
		},
		DisplayTerminalID: "call-1",
		Scope:             eventstream.ScopeMain,
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
	assertStreamDelivery(t, env, true, false, false)
}

func TestProjectStreamFramePreservesClosedCommandExitCode(t *testing.T) {
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
	events := ProjectStreamFrame(req, stream.Frame{
		Ref:      req.Ref,
		Cursor:   stream.Cursor{Output: 3},
		Closed:   true,
		State:    "failed",
		ExitCode: &exitCode,
	})
	if len(events) != 1 {
		t.Fatalf("ProjectStreamFrame() returned %d events: %#v", len(events), events)
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
}

func TestProjectStreamFramePreservesSplitNewlineFrame(t *testing.T) {
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
		events := ProjectStreamFrame(req, frame)
		if len(events) != 1 {
			t.Fatalf("ProjectStreamFrame(%q) returned %d events: %#v", frame.Text, len(events), events)
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

func TestProjectStreamFrameFinalDoesNotRepeatStreamedOutput(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		SessionRef: session.SessionRef{SessionID: "root-session"},
		CallID:     "command-1",
		ToolName:   "RUN_COMMAND",
		RawInput:   map[string]any{"command": "printf ok"},
		Ref:        stream.Ref{SessionID: "root-session", TaskID: "task-1", TerminalID: "terminal-1"},
		Scope:      eventstream.ScopeMain,
	}
	events := ProjectStreamFrame(req, stream.Frame{
		Ref:     req.Ref,
		Text:    "ok\n",
		Cursor:  stream.Cursor{Output: 3},
		Closed:  true,
		Running: false,
		State:   "completed",
	})
	if len(events) != 1 {
		t.Fatalf("ProjectStreamFrame(RUN_COMMAND closed) returned %d events: %#v", len(events), events)
	}
	update := requireToolUpdate(t, events[0])
	if got := stringPtrValue(update.Status); got != schema.ToolStatusCompleted {
		t.Fatalf("status = %q, want completed", got)
	}
	assertTerminalAnchor(t, update.Content, "command-1")
}

func TestProjectStreamFrameMarksEmbeddedEventsMirroredToParentTool(t *testing.T) {
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
	events := ProjectStreamFrame(req, stream.Frame{
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
	if len(events) != 2 {
		t.Fatalf("ProjectStreamFrame() returned %d events: %#v, want embedded child event plus parent tool update", len(events), events)
	}
	embedded := events[0]
	if embedded.Scope != eventstream.ScopeSubagent || embedded.ScopeID != "jack" || eventstream.UpdateType(embedded.Update) != schema.UpdateAgentMessage {
		t.Fatalf("embedded event = %#v, want subagent agent message", embedded)
	}
	if embedded.ParentTool == nil || embedded.ParentTool.ToolCallID != "task-call-1" || embedded.ParentTool.ToolName != "TASK" {
		t.Fatalf("embedded parent relation = %#v, want TASK/task-call-1", embedded.ParentTool)
	}
	assertStreamDelivery(t, embedded, true, true, false)
	assertNoLegacyRelationDeliveryMetadata(t, embedded)
	update := requireToolUpdate(t, events[1])
	if update.ToolCallID != "task-call-1" || toolTerminalOutputText(t, update) != "child output\n" {
		t.Fatalf("parent tool update = %#v, want child output in parent panel", update)
	}
	assertStreamDelivery(t, events[1], true, false, true)
}

func TestProjectStreamFrameAppendsSubagentReasoningToParentTerminal(t *testing.T) {
	t.Parallel()

	req := StreamRequest{
		SessionRef: session.SessionRef{SessionID: "root-session"},
		CallID:     "spawn-1",
		ToolName:   "SPAWN",
		RawInput:   map[string]any{"agent": "self", "prompt": "demo"},
		Ref:        stream.Ref{SessionID: "root-session", TaskID: "amy"},
		Scope:      eventstream.ScopeMain,
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
	if len(events) != 1 {
		t.Fatalf("reasoning frame events = %#v, want one parent tool update", events)
	}
	update := requireToolUpdate(t, events[0])
	if update.ToolCallID != "spawn-1" {
		t.Fatalf("tool update = %#v, want parent spawn call", update)
	}
	if got := toolTerminalOutputText(t, update); got != "The user wants me to inspect files." {
		t.Fatalf("terminal output = %q, want reasoning text", got)
	}

	events = ProjectStreamFrame(req, stream.Frame{
		Ref:     req.Ref,
		Text:    "final visible output",
		Running: true,
	})
	if len(events) != 1 {
		t.Fatalf("stdout frame events = %#v, want one parent tool update", events)
	}
	update = requireToolUpdate(t, events[0])
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
		Scope:      eventstream.ScopeMain,
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
	if stringPtrValue(update.Status) != schema.ToolStatusFailed {
		t.Fatalf("update = %+v, want failed RUN_COMMAND result", update)
	}
	if strings.Contains(toolTerminalOutputText(t, update), "exit 1") {
		t.Fatalf("meta = %#v, should not expose exit code as terminal output", update.Meta)
	}
	if got := toolTerminalOutputText(t, update); got != "(no output)" {
		t.Fatalf("terminal output = %q, want no-output placeholder", got)
	}
}

func TestProjectStreamFrameProjectsSubagentSemanticEventBeforeParentTerminal(t *testing.T) {
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

	events := ProjectStreamFrame(req, frame)
	if len(events) != 2 {
		t.Fatalf("ProjectStreamFrame() returned %d events, want child semantic event then parent terminal update: %#v", len(events), events)
	}
	assertSpawnSemanticEnvelope(t, events[0], "jack", "spawn-call-1", true)
	message, ok := events[0].Update.(schema.ContentChunk)
	if !ok {
		t.Fatalf("child update = %T, want ContentChunk", events[0].Update)
	}
	content, ok := message.Content.(schema.TextContent)
	if !ok || message.SessionUpdate != schema.UpdateAgentMessage || message.MessageID != "child-message-1" || content.Text != "The user wants a file" {
		t.Fatalf("child message = %#v, want original ACP message chunk fields", message)
	}
	tool := requireToolUpdate(t, events[1])
	if got := toolTerminalOutputText(t, tool); got != "The user wants a file" {
		t.Fatalf("terminal output = %q, want original stream text", got)
	}
	assertTerminalAnchor(t, tool.Content, "spawn-call-1")
	assertStreamTerminalInfo(t, tool.Meta, "spawn-call-1")
	assertStreamDelivery(t, events[1], true, false, true)
}

func TestProjectStreamFrameProjectsEventOnlySpawnChildSemantics(t *testing.T) {
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
			events := ProjectStreamFrame(req, stream.Frame{
				Ref:     req.Ref,
				Running: true,
				Event:   tc.event,
			})
			if len(events) != 1 {
				t.Fatalf("ProjectStreamFrame() returned %d events, want one child semantic event: %#v", len(events), events)
			}
			assertSpawnSemanticEnvelope(t, events[0], "jack", "spawn-call-1", false)
			tc.assert(t, events[0])
		})
	}
}

func TestProjectStreamFrameRetainsSpawnTextCompatibilityWithoutEvent(t *testing.T) {
	t.Parallel()

	req := spawnStreamRequestForTest()
	events := ProjectStreamFrame(req, stream.Frame{
		Ref:     req.Ref,
		Text:    "child stream text\n",
		Running: true,
	})
	if len(events) != 1 {
		t.Fatalf("ProjectStreamFrame(text-only) returned %d events, want parent terminal mirror: %#v", len(events), events)
	}
	if update := requireToolUpdate(t, events[0]); toolTerminalOutputText(t, update) != "child stream text\n" {
		t.Fatalf("parent text mirror = %#v, want child stream text", update)
	}

	events = ProjectStreamFrame(req, stream.Frame{Ref: req.Ref, Running: true})
	if len(events) != 0 {
		t.Fatalf("ProjectStreamFrame(empty) = %#v, want no output", events)
	}
}

func TestProjectStreamFrameProjectsClosedSpawnEventBeforeOneParentFinal(t *testing.T) {
	t.Parallel()

	req := spawnStreamRequestForTest()
	events := ProjectStreamFrame(req, stream.Frame{
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
	if len(events) != 2 {
		t.Fatalf("ProjectStreamFrame() returned %d events, want child semantic event and one parent final: %#v", len(events), events)
	}
	assertSpawnSemanticEnvelope(t, events[0], "jack", "spawn-call-1", false)
	if update, ok := events[0].Update.(schema.ContentChunk); !ok || update.MessageID != "child-message-final" {
		t.Fatalf("closed child semantic update = %#v, want final child message", events[0].Update)
	}
	final := requireToolUpdate(t, events[1])
	if got := stringPtrValue(final.Status); got != schema.ToolStatusCompleted {
		t.Fatalf("parent final status = %q, want completed", got)
	}
	if _, ok := metautil.TerminalOutput(final.Meta); ok {
		t.Fatalf("parent final meta = %#v, must not replay consumed child text", final.Meta)
	}
}

func TestProjectStreamFrameSubagentFinalStatesDoNotReplayStreamedOutput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		state      string
		wantStatus string
	}{
		{state: "completed", wantStatus: schema.ToolStatusCompleted},
		{state: "failed", wantStatus: schema.ToolStatusFailed},
		// ACP tool updates have no cancelled status variant; the standard failed
		// status is paired with the explicit Caelis task state below.
		{state: "cancelled", wantStatus: schema.ToolStatusFailed},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.state, func(t *testing.T) {
			req := spawnStreamRequestForTest()
			events := ProjectStreamFrame(req, stream.Frame{
				Ref:    req.Ref,
				Text:   "### accumulated child output\n",
				Cursor: stream.Cursor{Output: 27},
				Closed: true,
				State:  tc.state,
			})
			if len(events) != 1 {
				t.Fatalf("ProjectStreamFrame(%s) returned %d events, want one parent final: %#v", tc.state, len(events), events)
			}
			final := requireToolUpdate(t, events[0])
			if got := stringPtrValue(final.Status); got != tc.wantStatus {
				t.Fatalf("parent final status = %q, want %q", got, tc.wantStatus)
			}
			if got := runtimeTaskMeta(final.Meta)["state"]; got != tc.state {
				t.Fatalf("parent final task state = %#v, want %q", got, tc.state)
			}
			if _, ok := metautil.TerminalOutput(final.Meta); ok {
				t.Fatalf("parent final meta = %#v, must not replay consumed child text", final.Meta)
			}
		})
	}
}

func TestProjectStreamFrameProjectsSubagentFinalToParentTerminal(t *testing.T) {
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
	events := ProjectStreamFrame(req, stream.Frame{
		Ref:     stream.Ref{SessionID: "root-session", TaskID: "jack", TerminalID: "subagent-jack-turn-1"},
		Text:    "Final child result\n",
		Closed:  true,
		Running: false,
		State:   "completed",
	})
	if len(events) != 1 {
		t.Fatalf("ProjectStreamFrame() returned %d events, want parent terminal final update: %#v", len(events), events)
	}
	tool := requireToolUpdate(t, events[0])
	if stringPtrValue(tool.Status) != schema.ToolStatusCompleted {
		t.Fatalf("tool update = %#v, want completed SPAWN", tool)
	}
	if got := toolTerminalOutputText(t, tool); got != "Final child result" {
		t.Fatalf("terminal output = %q, want cleaned final result", got)
	}
	assertTerminalAnchor(t, tool.Content, "spawn-call-1")
	assertStreamTerminalInfo(t, tool.Meta, "spawn-call-1")
}

func TestProjectStreamFrameSubagentFinalWithStreamKeepsResultWithoutTerminalReplay(t *testing.T) {
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
	events := ProjectStreamFrame(req, stream.Frame{
		Ref:     stream.Ref{SessionID: "root-session", TaskID: "jack", TerminalID: "subagent-jack-turn-1"},
		Text:    "### Final child result\n",
		Cursor:  stream.Cursor{Output: 19},
		Closed:  true,
		Running: false,
		State:   "completed",
	})
	if len(events) != 1 {
		t.Fatalf("ProjectStreamFrame() returned %d events, want parent terminal final update: %#v", len(events), events)
	}
	tool := requireToolUpdate(t, events[0])
	if stringPtrValue(tool.Status) != schema.ToolStatusCompleted {
		t.Fatalf("tool update = %#v, want completed SPAWN", tool)
	}
	assertTerminalAnchor(t, tool.Content, "spawn-call-1")
	taskMeta := runtimeTaskMeta(tool.Meta)
	if got := taskMeta["result"]; got != "Final child result" {
		t.Fatalf("runtime task result = %#v, want cleaned final child result; meta=%#v", got, tool.Meta)
	}
	if got := taskMeta["running"]; got != false {
		t.Fatalf("runtime task running = %#v, want false; meta=%#v", got, tool.Meta)
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
		Scope:      eventstream.ScopeMain,
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

type streamFrameEnvelopeExpectation struct {
	parentCallID      string
	parentTool        string
	transient         bool
	hasMirror         bool
	isMirror          bool
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
	assertStreamDelivery(t, env, want.transient, want.hasMirror, want.isMirror)
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

func assertSpawnSemanticEnvelope(t *testing.T, env eventstream.Envelope, taskID string, parentCallID string, hasParentToolMirror bool) {
	t.Helper()
	if env.Kind != eventstream.KindSessionUpdate || env.Scope != eventstream.ScopeSubagent || env.ScopeID != taskID {
		t.Fatalf("child envelope = %#v, want scoped subagent session/update for task %q", env, taskID)
	}
	if env.ParentTool == nil || env.ParentTool.ToolCallID != parentCallID || env.ParentTool.ToolName != "SPAWN" {
		t.Fatalf("child parent relation = %#v, want SPAWN/%q", env.ParentTool, parentCallID)
	}
	assertStreamDelivery(t, env, true, hasParentToolMirror, false)
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
	if metautil.Bool(env.Meta, metautil.Root, metautil.Runtime, metautil.RuntimeStream, metautil.RuntimeStreamMirroredToParentTool) ||
		metautil.Bool(env.Meta, metautil.Root, metautil.Transient) {
		t.Fatalf("envelope meta = %#v, want no legacy delivery shadow", env.Meta)
	}
}

func assertStreamDelivery(t *testing.T, env eventstream.Envelope, transient bool, hasParentToolMirror bool, isParentToolMirror bool) {
	t.Helper()
	if env.Delivery == nil {
		t.Fatalf("envelope delivery = nil, want transient=%t has_parent_tool_mirror=%t is_parent_tool_mirror=%t", transient, hasParentToolMirror, isParentToolMirror)
	}
	if env.Delivery.Transient != transient || env.Delivery.HasParentToolMirror != hasParentToolMirror || env.Delivery.IsParentToolMirror != isParentToolMirror {
		t.Fatalf("envelope delivery = %#v, want transient=%t has_parent_tool_mirror=%t is_parent_tool_mirror=%t", env.Delivery, transient, hasParentToolMirror, isParentToolMirror)
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
