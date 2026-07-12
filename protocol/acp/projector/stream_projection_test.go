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
		ToolCallID:         "call-1",
		ToolName:           "RUN_COMMAND",
		RawInput:           map[string]any{"command": "go test ./..."},
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
	if !metautil.Bool(update.Meta, metautil.Root, metautil.Transient) {
		t.Fatalf("update.Meta = %#v, want transient stream update", update.Meta)
	}
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
	if got := metautil.String(embedded.Meta, metautil.Root, metautil.Runtime, metautil.RuntimeStream, metautil.RuntimeStreamParentCallID); got != "task-call-1" {
		t.Fatalf("embedded meta parent_call_id = %q, want task-call-1; meta=%#v", got, embedded.Meta)
	}
	if !metautil.Bool(embedded.Meta, metautil.Root, metautil.Runtime, metautil.RuntimeStream, metautil.RuntimeStreamMirroredToParentTool) {
		t.Fatalf("embedded meta = %#v, want mirrored_to_parent_tool=true", embedded.Meta)
	}
	update := requireToolUpdate(t, events[1])
	if update.ToolCallID != "task-call-1" || toolTerminalOutputText(t, update) != "child output\n" {
		t.Fatalf("parent tool update = %#v, want child output in parent panel", update)
	}
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

func TestProjectStreamFrameProjectsSubagentStreamToParentTerminalOnly(t *testing.T) {
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
	if len(events) != 1 {
		t.Fatalf("ProjectStreamFrame() returned %d events, want parent terminal update only: %#v", len(events), events)
	}
	if eventstream.UpdateType(events[0].Update) != schema.UpdateToolCallInfo {
		t.Fatalf("event update = %#v, want tool_call_update", events[0].Update)
	}
	tool := requireToolUpdate(t, events[0])
	if got := toolTerminalOutputText(t, tool); got != "The user wants a file" {
		t.Fatalf("terminal output = %q, want original stream text", got)
	}
	assertTerminalAnchor(t, tool.Content, "spawn-call-1")
	assertStreamTerminalInfo(t, tool.Meta, "spawn-call-1")
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
