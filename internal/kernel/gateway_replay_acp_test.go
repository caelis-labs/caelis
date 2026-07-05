package kernel

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestReplayEventsMatchesLiveACPProjectionSemantics(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	assistant := model.NewMessage(
		model.RoleAssistant,
		model.NewReasoningPart("thinking", model.ReasoningVisibilityVisible),
		model.NewTextPart("done"),
	)
	toolCall := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
		ID:   "call-1",
		Name: "RUN_COMMAND",
		Args: `{"command":"pwd"}`,
	}}, "")
	events := []*session.Event{
		{
			ID:         "assistant-1",
			SessionID:  "s1",
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(10, 0),
			Message:    &assistant,
			Scope:      &session.EventScope{Source: "acp", TurnID: "turn-1"},
			Meta: map[string]any{
				"usage": map[string]any{
					"prompt_tokens":       12,
					"cached_input_tokens": 3,
					"completion_tokens":   5,
					"reasoning_tokens":    2,
					"total_tokens":        17,
				},
			},
		},
		{
			ID:         "tool-call-1",
			SessionID:  "s1",
			Type:       session.EventTypeToolCall,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(11, 0),
			Message:    &toolCall,
			Scope:      &session.EventScope{Source: "acp", TurnID: "turn-1"},
			Tool: &session.EventTool{
				ID:     "call-1",
				Name:   "RUN_COMMAND",
				Kind:   schema.ToolKindExecute,
				Title:  "pwd",
				Status: "pending",
				Input:  map[string]any{"command": "pwd"},
			},
		},
		{
			ID:         "tool-result-1",
			SessionID:  "s1",
			Type:       session.EventTypeToolResult,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(12, 0),
			Scope:      &session.EventScope{Source: "acp", TurnID: "turn-1"},
			Tool: &session.EventTool{
				ID:     "call-1",
				Name:   "RUN_COMMAND",
				Kind:   schema.ToolKindExecute,
				Title:  "pwd",
				Status: "completed",
				Input:  map[string]any{"command": "pwd"},
				Output: map[string]any{"stdout": "/tmp\n"},
			},
		},
		{
			ID:         "plan-1",
			SessionID:  "s1",
			Type:       session.EventTypePlan,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(13, 0),
			Scope:      &session.EventScope{Source: "acp", TurnID: "turn-1"},
			PlanPayload: &session.EventPlanPayload{Entries: []session.EventPlanEntry{{
				Content:  "Run tests",
				Status:   "completed",
				Priority: "high",
			}}},
		},
		{
			ID:         "permission-1",
			SessionID:  "s1",
			Type:       session.EventTypeLifecycle,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(13, 500000000),
			Scope:      &session.EventScope{Source: "acp", TurnID: "turn-1"},
			Protocol: &session.EventProtocol{
				Method: session.ProtocolMethodRequestPermission,
				Permission: &session.ProtocolApproval{
					ToolCall: session.ProtocolToolCall{
						ID:       "call-approve",
						Name:     "RUN_COMMAND",
						Kind:     schema.ToolKindExecute,
						Title:    "RUN_COMMAND rm",
						Status:   "pending",
						RawInput: map[string]any{"command": "rm -rf tmp"},
					},
					Options: []session.ProtocolApprovalOption{
						{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
						{ID: "reject_once", Name: "Reject once", Kind: "reject_once"},
					},
				},
			},
		},
		{
			ID:         "participant-1",
			SessionID:  "s1",
			Type:       session.EventTypeParticipant,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(14, 0),
			Actor:      session.ActorRef{Kind: session.ActorKindParticipant, ID: "agent-1", Name: "@agent"},
			Scope: &session.EventScope{
				Source:      "acp_participant",
				TurnID:      "turn-1",
				Participant: session.ParticipantRef{ID: "agent-1", Kind: session.ParticipantKindACP, Role: session.ParticipantRoleSidecar},
			},
			Protocol: &session.EventProtocol{
				Method: session.ProtocolMethodParticipantUpdate,
				Update: &session.ProtocolUpdate{SessionUpdate: "attached"},
			},
		},
		{
			ID:         "handoff-1",
			SessionID:  "s1",
			Type:       session.EventTypeHandoff,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(14, 500000000),
			Actor:      session.ActorRef{Kind: session.ActorKindSystem, Name: "runtime"},
			Scope:      &session.EventScope{Source: "handoff", TurnID: "turn-1"},
			Protocol: &session.EventProtocol{
				Method: session.ProtocolMethodControllerHandoff,
				Update: &session.ProtocolUpdate{SessionUpdate: "activation"},
			},
		},
		{
			ID:         "lifecycle-1",
			SessionID:  "s1",
			Type:       session.EventTypeLifecycle,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(15, 0),
			Actor:      session.ActorRef{Kind: session.ActorKindController, Name: "codex"},
			Scope:      &session.EventScope{Source: "acp", TurnID: "turn-1"},
			Lifecycle:  &session.EventLifecycle{Status: "completed", Reason: "done"},
		},
	}

	handle := newTurnHandle(turnHandleConfig{
		handleID:   "h1",
		runID:      "run-1",
		turnID:     "turn-1",
		sessionRef: activeSession.SessionRef,
		createdAt:  time.Unix(9, 0),
	})
	liveCh := handle.ACPEvents()
	for _, event := range events {
		handle.publishSessionEvent(event)
	}
	handle.finish()
	var live []eventstream.Envelope
	for env := range liveCh {
		live = append(live, env)
	}

	gw, err := New(Config{
		Sessions: &recordingSessionService{
			sessionResult: activeSession,
			eventsResult:  events,
		},
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	replayed, err := gw.ReplayEvents(context.Background(), ReplayEventsRequest{
		SessionRef:       activeSession.SessionRef,
		IncludeTransient: true,
	})
	if err != nil {
		t.Fatalf("ReplayEvents() error = %v", err)
	}

	got := semanticACPEnvelopes(replayed.Events)
	want := semanticACPEnvelopes(live)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReplayEvents semantic envelopes differ from live ACPEvents\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestReplayEventsLimitDoesNotSplitProjectedACPEnvelopes(t *testing.T) {
	t.Parallel()

	activeSession, events := replayCursorFixtureEvents()
	gw, err := New(Config{
		Sessions: &recordingSessionService{
			sessionResult: activeSession,
			eventsResult:  events,
		},
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	replayed, err := gw.ReplayEvents(context.Background(), ReplayEventsRequest{
		SessionRef: activeSession.SessionRef,
		Cursor:     "e1",
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("ReplayEvents() error = %v", err)
	}
	if len(replayed.Events) != 2 {
		t.Fatalf("ReplayEvents().Events len = %d, want full thought + message group: %#v", len(replayed.Events), replayed.Events)
	}
	if replayed.Events[0].Cursor != "acp-projection:ZTI:0" || replayed.Events[1].Cursor != "acp-projection:ZTI:1" || replayed.NextCursor != "acp-projection:ZTI:1" {
		t.Fatalf("replay cursors = [%q %q] next=%q, want e2 projection cursors", replayed.Events[0].Cursor, replayed.Events[1].Cursor, replayed.NextCursor)
	}
	if replayed.Events[0].EventID != "e2" || replayed.Events[1].ProjectionID != "acp-projection:ZTI:1" {
		t.Fatalf("replay projection ids = %#v %#v, want event_id/projection_id", replayed.Events[0], replayed.Events[1])
	}
	if eventstream.UpdateType(replayed.Events[0].Update) != schema.UpdateAgentThought ||
		eventstream.UpdateType(replayed.Events[1].Update) != schema.UpdateAgentMessage {
		t.Fatalf("replay updates = %#v %#v, want thought + message", replayed.Events[0].Update, replayed.Events[1].Update)
	}
}

func TestReplayEventsResumesWithinMultiEnvelopeProjection(t *testing.T) {
	t.Parallel()

	activeSession, events := replayCursorFixtureEvents()
	gw, err := New(Config{
		Sessions: &recordingSessionService{
			sessionResult: activeSession,
			eventsResult:  events,
		},
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	replayed, err := gw.ReplayEvents(context.Background(), ReplayEventsRequest{
		SessionRef: activeSession.SessionRef,
		Cursor:     "acp-projection:ZTI:0",
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("ReplayEvents() error = %v", err)
	}
	if len(replayed.Events) != 1 {
		t.Fatalf("ReplayEvents().Events len = %d, want remaining message projection: %#v", len(replayed.Events), replayed.Events)
	}
	if replayed.Events[0].Cursor != "acp-projection:ZTI:1" ||
		replayed.Events[0].EventID != "e2" ||
		replayed.Events[0].ProjectionID != "acp-projection:ZTI:1" ||
		eventstream.UpdateType(replayed.Events[0].Update) != schema.UpdateAgentMessage {
		t.Fatalf("ReplayEvents().Events[0] = %#v, want remaining e2 message projection", replayed.Events[0])
	}
	if replayed.NextCursor != "acp-projection:ZTI:1" {
		t.Fatalf("ReplayEvents().NextCursor = %q, want e2 projection cursor", replayed.NextCursor)
	}

	replayed, err = gw.ReplayEvents(context.Background(), ReplayEventsRequest{
		SessionRef: activeSession.SessionRef,
		Cursor:     "acp-projection:ZTI:1",
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("ReplayEvents(after final projection) error = %v", err)
	}
	if len(replayed.Events) != 1 ||
		replayed.Events[0].Cursor != "acp-projection:ZTM:0" ||
		replayed.Events[0].EventID != "e3" ||
		eventstream.UpdateType(replayed.Events[0].Update) != schema.UpdateAgentMessage {
		t.Fatalf("ReplayEvents(after final projection).Events = %#v, want e3 message projection", replayed.Events)
	}
	if replayed.NextCursor != "acp-projection:ZTM:0" {
		t.Fatalf("ReplayEvents(after final projection).NextCursor = %q, want e3 projection cursor", replayed.NextCursor)
	}
}

func TestReplayEventsAcceptsLiveProjectionIDAsResumeCursor(t *testing.T) {
	t.Parallel()

	activeSession, events := replayCursorFixtureEvents()
	handle := newTurnHandle(turnHandleConfig{
		handleID:   "h1",
		runID:      "run-1",
		turnID:     "turn-1",
		sessionRef: activeSession.SessionRef,
		createdAt:  time.Unix(9, 0),
	})
	liveCh := handle.ACPEvents()
	handle.publishSessionEvent(events[1])
	handle.finish()
	var live []eventstream.Envelope
	for env := range liveCh {
		live = append(live, env)
	}
	if len(live) < 2 || live[0].Cursor == live[0].ProjectionID || live[0].ProjectionID != "acp-projection:ZTI:0" {
		t.Fatalf("live ACP identity = %#v, want stream cursor plus durable e2 projection_id", live)
	}

	gw, err := New(Config{
		Sessions: &recordingSessionService{
			sessionResult: activeSession,
			eventsResult:  events,
		},
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	replayed, err := gw.ReplayEvents(context.Background(), ReplayEventsRequest{
		SessionRef: activeSession.SessionRef,
		Cursor:     live[0].ProjectionID,
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("ReplayEvents(live projection_id) error = %v", err)
	}
	if len(replayed.Events) != 1 ||
		replayed.Events[0].Cursor != "acp-projection:ZTI:1" ||
		replayed.Events[0].ProjectionID != "acp-projection:ZTI:1" ||
		eventstream.UpdateType(replayed.Events[0].Update) != schema.UpdateAgentMessage {
		t.Fatalf("ReplayEvents(live projection_id).Events = %#v, want remaining e2 message projection", replayed.Events)
	}
}

func TestReplayEventsRestoresCompletedSubagentTaskPanel(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	events := []*session.Event{
		{
			ID:         "spawn-call",
			SessionID:  "s1",
			Type:       session.EventTypeToolCall,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(20, 0),
			Scope:      &session.EventScope{Source: "acp", TurnID: "turn-1"},
			Tool: &session.EventTool{
				ID:     "spawn-1",
				Name:   "SPAWN",
				Kind:   schema.ToolKindExecute,
				Title:  "SPAWN explorer",
				Status: "pending",
				Input:  map[string]any{"agent": "explorer", "prompt": "inspect"},
			},
			Meta: map[string]any{
				EventMetaRoot: map[string]any{
					EventMetaRuntime: map[string]any{
						EventMetaRuntimeTool: map[string]any{EventMetaRuntimeToolName: "SPAWN"},
					},
				},
			},
		},
		{
			ID:         "spawn-running",
			SessionID:  "s1",
			Type:       session.EventTypeToolResult,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(21, 0),
			Scope:      &session.EventScope{Source: "acp", TurnID: "turn-1"},
			Tool: &session.EventTool{
				ID:     "spawn-1",
				Name:   "SPAWN",
				Kind:   schema.ToolKindExecute,
				Title:  "SPAWN explorer",
				Status: "running",
				Input:  map[string]any{"agent": "explorer", "prompt": "inspect"},
				Output: map[string]any{"state": "running", "task_id": "akio"},
			},
			Meta: map[string]any{
				EventMetaRoot: map[string]any{
					EventMetaRuntime: map[string]any{
						EventMetaRuntimeTool: map[string]any{EventMetaRuntimeToolName: "SPAWN"},
						EventMetaRuntimeTask: map[string]any{
							"kind":                         string(taskapi.KindSubagent),
							EventMetaRuntimeTaskID:         "akio",
							EventMetaRuntimeTaskTerminalID: "subagent-task-1",
							"handle":                       "akio",
							"session_id":                   "child-session",
							"running":                      true,
							"state":                        "running",
						},
					},
				},
			},
		},
		{
			ID:         "task-wait-completed",
			SessionID:  "s1",
			Type:       session.EventTypeToolResult,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(23, 0),
			Scope:      &session.EventScope{Source: "acp", TurnID: "turn-1"},
			Tool: &session.EventTool{
				ID:     "task-wait-1",
				Name:   "TASK",
				Kind:   schema.ToolKindExecute,
				Title:  "TASK wait akio",
				Status: "completed",
				Input:  map[string]any{"action": "wait", "task_id": "akio"},
				Output: map[string]any{"state": "completed", "task_id": "akio", "final_message": "child final answer"},
			},
			Meta: map[string]any{
				EventMetaRoot: map[string]any{
					EventMetaRuntime: map[string]any{
						EventMetaRuntimeTool: map[string]any{
							EventMetaRuntimeToolName:   "TASK",
							EventMetaRuntimeTargetKind: string(taskapi.KindSubagent),
							EventMetaRuntimeTargetID:   "akio",
							EventMetaRuntimeToolAction: "wait",
						},
						EventMetaRuntimeTask: map[string]any{
							"kind":                 string(taskapi.KindSubagent),
							EventMetaRuntimeTaskID: "akio",
							"handle":               "akio",
							"running":              false,
							"state":                "completed",
							"final_message":        "child final answer",
						},
					},
				},
			},
		},
	}
	taskEntry := &taskapi.Entry{
		TaskID:    "internal-task-1",
		Kind:      taskapi.KindSubagent,
		Session:   activeSession.SessionRef,
		State:     taskapi.StateCompleted,
		Running:   false,
		CreatedAt: time.Unix(20, 0),
		UpdatedAt: time.Unix(22, 0),
		Spec: map[string]any{
			"agent":       "explorer",
			"handle":      "akio",
			"session_id":  "child-session",
			"terminal_id": "subagent-task-1",
		},
		Result: map[string]any{
			"task_id":       "akio",
			"handle":        "akio",
			"session_id":    "child-session",
			"terminal_id":   "subagent-task-1",
			"final_message": "child final answer",
			"result":        "child final answer",
		},
	}
	gw, err := New(Config{
		Sessions: &recordingSessionService{
			sessionResult: activeSession,
			eventsResult:  events,
		},
		Tasks:    replayTaskStore{entries: []*taskapi.Entry{taskEntry}},
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	replayed, err := gw.ReplayEvents(context.Background(), ReplayEventsRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("ReplayEvents() error = %v", err)
	}
	if len(replayed.Events) != 4 {
		t.Fatalf("ReplayEvents().Events = %#v, want call, running update, restored final update, completed TASK update", replayed.Events)
	}
	if replayed.NextCursor != replayed.Events[3].Cursor || strings.TrimSpace(replayed.NextCursor) == "" {
		t.Fatalf("ReplayEvents().NextCursor = %q, want last durable event cursor %q", replayed.NextCursor, replayed.Events[3].Cursor)
	}
	restored := replayed.Events[2]
	if restored.EventID != "" || restored.Cursor != "" || restored.ProjectionID != "" || !restored.Final || len(restored.Meta) != 0 {
		t.Fatalf("restored envelope identity = %#v, want replay-only synthetic final update", restored)
	}
	update, ok := eventstream.ToolCallUpdateFromEnvelope(restored)
	if !ok {
		t.Fatalf("restored update = %#v, want tool_call_update", restored.Update)
	}
	if update.ToolCallID != "spawn-1" || stringPtrValue(update.Status) != schema.ToolStatusCompleted {
		t.Fatalf("restored update = %#v, want completed spawn-1", update)
	}
	rawOutput := schema.NormalizeRawMap(update.RawOutput)
	if rawOutput["final_message"] != "child final answer" || rawOutput["task_id"] != "akio" {
		t.Fatalf("restored raw output = %#v, want task final output", rawOutput)
	}
	if got := EventMetaString(update.Meta, EventMetaRoot, EventMetaRuntime, EventMetaRuntimeTool, EventMetaRuntimeToolName); got != "SPAWN" {
		t.Fatalf("restored runtime tool name = %q, want SPAWN from parent tool metadata; meta=%#v", got, update.Meta)
	}
	if output, ok := metautil.TerminalOutput(update.Meta); !ok || output.TerminalID != "spawn-1" || output.Data != "child final answer" {
		t.Fatalf("restored terminal output = %#v ok=%v, want spawn panel final answer; meta=%#v", output, ok, update.Meta)
	}
}

func TestReplayEventsRestoresCompletedCommandTaskPanel(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	command := `for i in {1..2}; do echo "BASH task running ($i/2)"; sleep 1; done; echo "BASH task complete"`
	events := []*session.Event{
		{
			ID:         "cmd-call",
			SessionID:  "s1",
			Type:       session.EventTypeToolCall,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(30, 0),
			Scope:      &session.EventScope{Source: "acp", TurnID: "turn-1"},
			Tool: &session.EventTool{
				ID:     "cmd-1",
				Name:   "RUN_COMMAND",
				Kind:   schema.ToolKindExecute,
				Title:  "RUN_COMMAND bash loop",
				Status: "pending",
				Input:  map[string]any{"command": command},
			},
			Meta: map[string]any{
				EventMetaRoot: map[string]any{
					EventMetaRuntime: map[string]any{
						EventMetaRuntimeTool: map[string]any{EventMetaRuntimeToolName: "RUN_COMMAND"},
					},
				},
			},
		},
		{
			ID:         "cmd-running",
			SessionID:  "s1",
			Type:       session.EventTypeToolResult,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(31, 0),
			Scope:      &session.EventScope{Source: "acp", TurnID: "turn-1"},
			Tool: &session.EventTool{
				ID:     "cmd-1",
				Name:   "RUN_COMMAND",
				Kind:   schema.ToolKindExecute,
				Title:  "RUN_COMMAND bash loop",
				Status: "running",
				Input:  map[string]any{"command": command},
				Output: map[string]any{"state": "running", "task_id": "cmd-task-1", "latest_output": "BASH task running (1/2)\n"},
			},
			Meta: map[string]any{
				EventMetaRoot: map[string]any{
					EventMetaRuntime: map[string]any{
						EventMetaRuntimeTool: map[string]any{EventMetaRuntimeToolName: "RUN_COMMAND"},
						EventMetaRuntimeTask: map[string]any{
							"kind":                         string(taskapi.KindCommand),
							EventMetaRuntimeTaskID:         "cmd-task-1",
							EventMetaRuntimeTaskTerminalID: "cmd-terminal",
							"session_id":                   "cmd-session",
							"running":                      true,
							"state":                        "running",
						},
					},
				},
			},
		},
	}
	indexEntry := &taskapi.Entry{
		TaskID:    "cmd-task-1",
		Kind:      taskapi.KindCommand,
		Session:   activeSession.SessionRef,
		Title:     "bash loop",
		State:     taskapi.StateCompleted,
		Running:   false,
		CreatedAt: time.Unix(30, 0),
		UpdatedAt: time.Unix(33, 0),
		Spec: map[string]any{
			"command":     command,
			"session_id":  "cmd-session",
			"terminal_id": "cmd-terminal",
		},
		Result: map[string]any{
			"state":     "completed",
			"exit_code": 0,
			"result":    "BASH task running (1/2)\nBASH task running (2/2)\nBASH task complete\n",
		},
		Metadata: map[string]any{
			"task_id":     "cmd-task-1",
			"task_kind":   string(taskapi.KindCommand),
			"session_id":  "cmd-session",
			"terminal_id": "cmd-terminal",
		},
	}
	gw, err := New(Config{
		Sessions: &recordingSessionService{
			sessionResult: activeSession,
			eventsResult:  events,
		},
		Tasks: replayTaskStore{
			entries: []*taskapi.Entry{indexEntry},
		},
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	replayed, err := gw.ReplayEvents(context.Background(), ReplayEventsRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("ReplayEvents() error = %v", err)
	}
	if len(replayed.Events) != 3 {
		t.Fatalf("ReplayEvents().Events = %#v, want call, running update, restored command final update", replayed.Events)
	}
	if replayed.NextCursor != replayed.Events[1].Cursor || strings.TrimSpace(replayed.NextCursor) == "" {
		t.Fatalf("ReplayEvents().NextCursor = %q, want last durable event cursor %q", replayed.NextCursor, replayed.Events[1].Cursor)
	}
	restored := replayed.Events[2]
	if restored.EventID != "" || restored.Cursor != "" || restored.ProjectionID != "" || !restored.Final || len(restored.Meta) != 0 {
		t.Fatalf("restored envelope identity = %#v, want replay-only synthetic final update", restored)
	}
	update, ok := eventstream.ToolCallUpdateFromEnvelope(restored)
	if !ok {
		t.Fatalf("restored update = %#v, want tool_call_update", restored.Update)
	}
	if update.ToolCallID != "cmd-1" || stringPtrValue(update.Status) != schema.ToolStatusCompleted {
		t.Fatalf("restored update = %#v, want completed cmd-1", update)
	}
	rawOutput := schema.NormalizeRawMap(update.RawOutput)
	wantOutput := "BASH task running (1/2)\nBASH task running (2/2)\nBASH task complete\n"
	if rawOutput["result"] != wantOutput || rawOutput["task_id"] != "cmd-task-1" || rawOutput["exit_code"] != 0 {
		t.Fatalf("restored raw output = %#v, want canonical command result", rawOutput)
	}
	if got := EventMetaString(update.Meta, EventMetaRoot, EventMetaRuntime, EventMetaRuntimeTool, EventMetaRuntimeToolName); got != "RUN_COMMAND" {
		t.Fatalf("restored runtime tool name = %q, want RUN_COMMAND from parent tool metadata; meta=%#v", got, update.Meta)
	}
	if got := EventMetaString(update.Meta, EventMetaRoot, EventMetaRuntime, EventMetaRuntimeTask, "kind"); got != string(taskapi.KindCommand) {
		t.Fatalf("restored task kind = %q, want command; meta=%#v", got, update.Meta)
	}
	if output, ok := metautil.TerminalOutput(update.Meta); !ok || output.TerminalID != "cmd-1" || output.Data != wantOutput {
		t.Fatalf("restored terminal output = %#v ok=%v, want command panel final output; meta=%#v", output, ok, update.Meta)
	}
}

func TestReplayEventsRestoresCompletedCommandTaskPanelAfterRunningCursor(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	command := `echo "BASH task complete"`
	events := []*session.Event{
		{
			ID:         "cmd-running",
			SessionID:  "s1",
			Type:       session.EventTypeToolResult,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(31, 0),
			Scope:      &session.EventScope{Source: "acp", TurnID: "turn-1"},
			Tool: &session.EventTool{
				ID:     "cmd-1",
				Name:   "RUN_COMMAND",
				Kind:   schema.ToolKindExecute,
				Title:  "RUN_COMMAND echo",
				Status: "running",
				Input:  map[string]any{"command": command},
				Output: map[string]any{"state": "running", "task_id": "cmd-task-1", "latest_output": "BASH task running\n"},
			},
			Meta: map[string]any{
				EventMetaRoot: map[string]any{
					EventMetaRuntime: map[string]any{
						EventMetaRuntimeTool: map[string]any{EventMetaRuntimeToolName: "RUN_COMMAND"},
						EventMetaRuntimeTask: map[string]any{
							EventMetaRuntimeTaskKind:       string(taskapi.KindCommand),
							EventMetaRuntimeTaskID:         "cmd-task-1",
							EventMetaRuntimeTaskTerminalID: "cmd-terminal",
							EventMetaRuntimeTaskSessionID:  "cmd-session",
							EventMetaRuntimeTaskRunning:    true,
							EventMetaRuntimeTaskState:      "running",
						},
					},
				},
			},
		},
	}
	taskEntry := &taskapi.Entry{
		TaskID:    "cmd-task-1",
		Kind:      taskapi.KindCommand,
		Session:   activeSession.SessionRef,
		Title:     "echo",
		State:     taskapi.StateCompleted,
		Running:   false,
		CreatedAt: time.Unix(30, 0),
		UpdatedAt: time.Unix(33, 0),
		Spec: map[string]any{
			"command":                      command,
			EventMetaRuntimeTaskSessionID:  "cmd-session",
			EventMetaRuntimeTaskTerminalID: "cmd-terminal",
		},
		Result: map[string]any{
			EventMetaRuntimeTaskState:  "completed",
			EventMetaRuntimeTaskResult: "BASH task complete\n",
			"exit_code":                0,
		},
		Metadata: map[string]any{
			EventMetaRuntimeTaskID:         "cmd-task-1",
			"task_kind":                    string(taskapi.KindCommand),
			EventMetaRuntimeTaskSessionID:  "cmd-session",
			EventMetaRuntimeTaskTerminalID: "cmd-terminal",
		},
	}
	gw, err := New(Config{
		Sessions: &recordingSessionService{
			sessionResult: activeSession,
			eventsResult:  events,
		},
		Tasks:    replayTaskStore{entries: []*taskapi.Entry{taskEntry}},
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	replayed, err := gw.ReplayEvents(context.Background(), ReplayEventsRequest{
		SessionRef: activeSession.SessionRef,
		Cursor:     formatACPProjectionCursor("cmd-running", 0),
	})
	if err != nil {
		t.Fatalf("ReplayEvents(after running cursor) error = %v", err)
	}
	if replayed.NextCursor != "" {
		t.Fatalf("ReplayEvents().NextCursor = %q, want no new durable cursor for synthetic final", replayed.NextCursor)
	}
	if len(replayed.Events) != 1 {
		t.Fatalf("ReplayEvents().Events = %#v, want synthetic final after running cursor", replayed.Events)
	}
	restored := replayed.Events[0]
	if restored.EventID != "" || restored.Cursor != "" || restored.ProjectionID != "" || !restored.Final || len(restored.Meta) != 0 {
		t.Fatalf("restored envelope identity = %#v, want replay-only synthetic final update with no stale envelope meta", restored)
	}
	update, ok := eventstream.ToolCallUpdateFromEnvelope(restored)
	if !ok {
		t.Fatalf("restored update = %#v, want tool_call_update", restored.Update)
	}
	if update.ToolCallID != "cmd-1" || stringPtrValue(update.Status) != schema.ToolStatusCompleted {
		t.Fatalf("restored update = %#v, want completed cmd-1", update)
	}
	if got := EventMetaString(update.Meta, EventMetaRoot, EventMetaRuntime, EventMetaRuntimeTask, EventMetaRuntimeTaskState); got != schema.ToolStatusCompleted {
		t.Fatalf("restored task state = %q, want completed; meta=%#v", got, update.Meta)
	}
	if EventMetaBool(update.Meta, EventMetaRoot, EventMetaRuntime, EventMetaRuntimeTask, EventMetaRuntimeTaskRunning) {
		t.Fatalf("restored task running meta = true, want false; meta=%#v", update.Meta)
	}
	if output, ok := metautil.TerminalOutput(update.Meta); !ok || output.TerminalID != "cmd-1" || output.Data != "BASH task complete\n" {
		t.Fatalf("restored terminal output = %#v ok=%v, want command final output; meta=%#v", output, ok, update.Meta)
	}
}

func TestReplayEventsProjectionCursorWinsOverCollidingSourceEventID(t *testing.T) {
	t.Parallel()

	activeSession, events := replayCursorFixtureEvents()
	projectionCursor := formatACPProjectionCursor("e2", 0)
	collision := &session.Event{
		ID:         projectionCursor,
		Type:       session.EventTypeAssistant,
		Text:       "collision",
		Message:    modelMessagePtr(model.NewTextMessage(model.RoleAssistant, "collision")),
		Visibility: session.VisibilityCanonical,
	}
	events = append(events[:2], append([]*session.Event{collision}, events[2:]...)...)
	gw, err := New(Config{
		Sessions: &recordingSessionService{
			sessionResult: activeSession,
			eventsResult:  events,
		},
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	replayed, err := gw.ReplayEvents(context.Background(), ReplayEventsRequest{
		SessionRef: activeSession.SessionRef,
		Cursor:     projectionCursor,
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("ReplayEvents(colliding projection cursor) error = %v", err)
	}
	if len(replayed.Events) != 1 ||
		replayed.Events[0].EventID != "e2" ||
		replayed.Events[0].ProjectionID != formatACPProjectionCursor("e2", 1) ||
		eventstream.UpdateType(replayed.Events[0].Update) != schema.UpdateAgentMessage {
		t.Fatalf("ReplayEvents(colliding projection cursor).Events = %#v, want remaining e2 projection", replayed.Events)
	}
}

func replayCursorFixtureEvents() (session.Session, []*session.Event) {
	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	message := model.NewMessage(
		model.RoleAssistant,
		model.NewReasoningPart("thinking", model.ReasoningVisibilityVisible),
		model.NewTextPart("done"),
	)
	events := []*session.Event{
		{ID: "e1", Type: session.EventTypeUser, Text: "first", Message: modelMessagePtr(model.NewTextMessage(model.RoleUser, "first"))},
		{ID: "e2", Type: session.EventTypeAssistant, Text: "done", Message: &message, Visibility: session.VisibilityCanonical},
		{ID: "e3", Type: session.EventTypeAssistant, Text: "third", Message: modelMessagePtr(model.NewTextMessage(model.RoleAssistant, "third")), Visibility: session.VisibilityCanonical},
	}
	return activeSession, events
}

type replayTaskStore struct {
	entries []*taskapi.Entry
}

func (s replayTaskStore) Upsert(context.Context, *taskapi.Entry) error {
	return nil
}

func (s replayTaskStore) Get(_ context.Context, taskID string) (*taskapi.Entry, error) {
	for _, entry := range s.entries {
		if entry != nil && strings.TrimSpace(entry.TaskID) == strings.TrimSpace(taskID) {
			return taskapi.CloneEntry(entry), nil
		}
	}
	return nil, nil
}

func (s replayTaskStore) ListSession(context.Context, session.SessionRef) ([]*taskapi.Entry, error) {
	out := make([]*taskapi.Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		out = append(out, taskapi.CloneEntry(entry))
	}
	return out, nil
}

func (s replayTaskStore) GetSessionTaskByHandle(_ context.Context, ref session.SessionRef, kind taskapi.Kind, handle string) (*taskapi.Entry, error) {
	handle = taskapi.NormalizeHandle(handle)
	for _, entry := range s.entries {
		if entry == nil || entry.Kind != kind {
			continue
		}
		if strings.TrimSpace(ref.SessionID) != "" && strings.TrimSpace(entry.Session.SessionID) != strings.TrimSpace(ref.SessionID) {
			continue
		}
		for _, value := range []string{
			replayAnyString(entry.Spec["handle"]),
			replayAnyString(entry.Metadata["handle"]),
			replayAnyString(entry.Result["handle"]),
		} {
			if taskapi.NormalizeHandle(value) == handle {
				return taskapi.CloneEntry(entry), nil
			}
		}
	}
	return nil, nil
}

type semanticACPEnvelope struct {
	Kind          eventstream.Kind
	EventID       string
	ProjectionID  string
	SessionID     string
	TurnID        string
	Scope         eventstream.Scope
	ScopeID       string
	Actor         string
	ParticipantID string
	Final         bool
	Update        semanticACPUpdate
	Permission    *schema.RequestPermissionRequest
	Participant   *eventstream.Participant
	Lifecycle     *eventstream.Lifecycle
	Meta          map[string]any
}

type semanticACPUpdate struct {
	Type       string
	Content    string
	MessageID  string
	ToolCallID string
	Kind       string
	Title      string
	Status     string
	RawInput   map[string]any
	RawOutput  map[string]any
	Entries    []schema.PlanEntry
	Meta       map[string]any
}

func semanticACPEnvelopes(events []eventstream.Envelope) []semanticACPEnvelope {
	out := make([]semanticACPEnvelope, 0, len(events))
	for _, env := range events {
		next := semanticACPEnvelope{
			Kind:          env.Kind,
			EventID:       env.EventID,
			ProjectionID:  env.ProjectionID,
			SessionID:     env.SessionID,
			TurnID:        env.TurnID,
			Scope:         env.Scope,
			ScopeID:       env.ScopeID,
			Actor:         env.Actor,
			ParticipantID: env.ParticipantID,
			Final:         env.Final,
			Update:        semanticACPUpdateOf(env.Update),
			Permission:    env.Permission,
			Participant:   env.Participant,
			Lifecycle:     env.Lifecycle,
			Meta:          semanticEnvelopeMeta(env.Meta),
		}
		out = append(out, next)
	}
	return out
}

func semanticEnvelopeMeta(meta map[string]any) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	out := make(map[string]any, len(meta))
	for key, value := range meta {
		if key != "caelis" {
			out[key] = value
			continue
		}
		caelis, ok := value.(map[string]any)
		if !ok {
			out[key] = value
			continue
		}
		cleaned := make(map[string]any, len(caelis))
		for caKey, caValue := range caelis {
			switch caKey {
			case "version", "bridge":
				continue
			default:
				cleaned[caKey] = caValue
			}
		}
		if len(cleaned) > 0 {
			out[key] = cleaned
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func semanticACPUpdateOf(update schema.Update) semanticACPUpdate {
	out := semanticACPUpdate{Type: eventstream.UpdateType(update)}
	switch typed := update.(type) {
	case schema.ContentChunk:
		out.Content = schema.ExtractTextValue(typed.Content)
		out.MessageID = typed.MessageID
		out.Meta = typed.Meta
	case schema.ToolCall:
		out.ToolCallID = typed.ToolCallID
		out.Kind = typed.Kind
		out.Title = typed.Title
		out.Status = typed.Status
		if raw, ok := typed.RawInput.(map[string]any); ok {
			out.RawInput = raw
		}
		if raw, ok := typed.RawOutput.(map[string]any); ok {
			out.RawOutput = raw
		}
		out.Meta = typed.Meta
	case schema.ToolCallUpdate:
		out.ToolCallID = typed.ToolCallID
		out.Kind = testDerefString(typed.Kind)
		out.Title = testDerefString(typed.Title)
		out.Status = testDerefString(typed.Status)
		if raw, ok := typed.RawInput.(map[string]any); ok {
			out.RawInput = raw
		}
		if raw, ok := typed.RawOutput.(map[string]any); ok {
			out.RawOutput = raw
		}
		out.Meta = typed.Meta
	case schema.PlanUpdate:
		out.Entries = typed.Entries
	case schema.UsageUpdate:
		out.RawInput = map[string]any{"size": typed.Size, "used": typed.Used}
		out.Meta = typed.Meta
	}
	return out
}

func testDerefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
