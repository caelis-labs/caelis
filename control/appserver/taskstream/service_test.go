package taskstream

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/internal/eventmeta"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
)

func TestProjectRecordPreservesStandardChildToolLifecycle(t *testing.T) {
	t.Parallel()

	descriptor := controltaskstream.TaskDescriptor{
		SessionID: "session-1", TaskID: "task-1", Handle: "grok-child", AgentHandle: "grok", Kind: task.KindSubagent,
		State: task.StateRunning, Running: true, CurrentTurnID: "child-turn-1",
		ParentTool: controltaskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "Spawn"},
	}
	completed := eventstream.ToolStatusCompleted
	tests := []struct {
		name       string
		eventType  session.EventType
		update     *session.ProtocolUpdate
		wantUpdate eventstream.Update
	}{
		{
			name:      "complete tool snapshot",
			eventType: session.EventTypeToolCall,
			update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolCall),
				ToolCallID:    "read-1",
				Title:         "Read `AGENTS.md`",
				Kind:          eventstream.ToolKindRead,
				Status:        eventstream.ToolStatusInProgress,
				RawInput:      map[string]any{"target_file": "AGENTS.md"},
			},
			wantUpdate: eventstream.ToolCall{
				SessionUpdate: eventstream.UpdateToolCall,
				ToolCallID:    "read-1",
				Title:         "Read `AGENTS.md`",
				Kind:          eventstream.ToolKindRead,
				Status:        eventstream.ToolStatusInProgress,
				RawInput:      map[string]any{"target_file": "AGENTS.md"},
			},
		},
		{
			name:      "sparse terminal patch",
			eventType: session.EventTypeToolResult,
			update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
				ToolCallID:    "read-1",
				Status:        completed,
			},
			wantUpdate: eventstream.ToolCallUpdate{
				SessionUpdate: eventstream.UpdateToolCallInfo,
				ToolCallID:    "read-1",
				Status:        &completed,
			},
		},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := controltaskstream.Record{
				Cursor: "cursor-" + tt.name, Generation: "generation-1", Sequence: uint64(index + 1), Task: descriptor,
				Frame: &controltaskstream.Frame{
					TerminalID: "child-turn-1",
					Running:    true,
					Event: &session.Event{
						Type: tt.eventType,
						Scope: &session.EventScope{Participant: session.ParticipantRef{
							ID: "grok-child", Kind: session.ParticipantKindSubagent, DelegationID: "task-1",
						}},
						Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: tt.update},
					},
				},
			}
			projected := projectRecord(record)
			if len(projected) != 1 {
				t.Fatalf("projectRecord() = %#v, want one tool envelope", projected)
			}
			if projected[0].Scope != eventstream.ScopeSubagent || projected[0].ScopeID != "task-1" ||
				projected[0].ParentTool == nil || projected[0].ParentTool.ToolCallID != "spawn-1" {
				t.Fatalf("projected child identity = %#v", projected[0])
			}
			if !reflect.DeepEqual(projected[0].Update, tt.wantUpdate) {
				t.Fatalf("projected tool update = %#v, want %#v", projected[0].Update, tt.wantUpdate)
			}
		})
	}
}

func TestIdenticalChildPayloadsRemainBoundToTheirTaskStreams(t *testing.T) {
	t.Parallel()

	for index, taskID := range []string{"task-1", "task-2", "task-3"} {
		record := controltaskstream.Record{
			Cursor: "cursor-" + taskID, Generation: "generation-1", Sequence: uint64(index + 1),
			Task: controltaskstream.TaskDescriptor{
				SessionID: "session-1", TaskID: taskID, Handle: "zuri-" + taskID, AgentHandle: "orbit", Kind: task.KindSubagent,
				State: task.StateRunning, Running: true, CurrentTurnID: "shared-turn",
				ParentTool: controltaskstream.ParentTool{ToolCallID: "spawn-" + taskID, ToolName: "Spawn"},
			},
			Frame: &controltaskstream.Frame{
				TerminalID: "shared-turn",
				Running:    true,
				Event: &session.Event{
					ID: "shared-event", Type: session.EventTypeAssistant,
					Scope: &session.EventScope{Participant: session.ParticipantRef{Kind: session.ParticipantKindSubagent}},
					Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{
						SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage), MessageID: "shared-message",
						Content: session.ProtocolTextContent("identical output"),
					}},
				},
			},
		}
		projected := projectRecord(record)
		if len(projected) != 1 || projected[0].Scope != eventstream.ScopeSubagent || projected[0].ScopeID != taskID {
			t.Fatalf("projectRecord(%s) = %#v, want one isolated Task envelope", taskID, projected)
		}
		if projected[0].ParentTool == nil || projected[0].ParentTool.ToolCallID != "spawn-"+taskID {
			t.Fatalf("projectRecord(%s) parent = %#v", taskID, projected[0].ParentTool)
		}
	}
}

func TestProjectRecordKeepsTaskScopeAndTransientCursor(t *testing.T) {
	record := controltaskstream.Record{
		Cursor: "cursor-7", Generation: "generation-1", Sequence: 7,
		Task: controltaskstream.TaskDescriptor{
			SessionID: "session-1", TaskID: "task-1", Handle: "zuri", AgentHandle: "orbit", Kind: task.KindSubagent,
			State: task.StateCompleted, ActivityID: "activity-2", CurrentTurnID: "turn-2",
			ParentTool: controltaskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "Spawn"},
		},
		Frame: &controltaskstream.Frame{
			TerminalID: "turn-2",
			State:      string(task.StateCompleted), Closed: true,
		},
	}

	projected := projectRecord(record)
	if len(projected) != 1 {
		t.Fatalf("projectRecord() = %#v, want one lifecycle envelope", projected)
	}
	envelope := projected[0]
	if envelope.Cursor != "cursor-7" || envelope.Scope != eventstream.ScopeSubagent ||
		envelope.ScopeID != "task-1" || envelope.ActivityID != "activity-2" {
		t.Fatalf("projected identity = %#v", envelope)
	}
	if envelope.Delivery == nil || envelope.Delivery.Mode != eventstream.DeliveryTransient || envelope.Position == nil || envelope.Position.Transient == nil {
		t.Fatalf("projected delivery = %#v", envelope)
	}
	if envelope.Position.Transient.Generation != "generation-1" || envelope.Position.Transient.Sequence != 7 {
		t.Fatalf("projected position = %#v", envelope.Position)
	}
	if envelope.ParentTool == nil || envelope.ParentTool.ToolCallID != "spawn-1" {
		t.Fatalf("projected parent tool = %#v", envelope.ParentTool)
	}
}

func TestReplacementDeliveryClearsRecordResumeIdentity(t *testing.T) {
	t.Parallel()

	delivery := projectDelivery(controltaskstream.Delivery{
		Kind: controltaskstream.DeliveryReplacePage, Source: controltaskstream.SourceReplacement,
		SnapshotID: "snapshot-1",
		Records: []controltaskstream.Record{{
			Cursor: "fallback-record-cursor", Generation: "fallback-generation", Sequence: 1,
			Task: controltaskstream.TaskDescriptor{
				SessionID: "session-1", TaskID: "task-1", Kind: task.KindSubagent,
				State: task.StateCompleted, CurrentTurnID: "turn-1",
				ParentTool: controltaskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "Spawn"},
			},
			Frame: &controltaskstream.Frame{
				TerminalID: "turn-1", State: string(task.StateCompleted), Closed: true,
			},
		}},
	})
	if len(delivery.Events) != 1 {
		t.Fatalf("replacement events = %#v", delivery.Events)
	}
	if delivery.Events[0].Cursor != "" || delivery.Events[0].Position != nil {
		t.Fatalf("replacement exposed record resume identity: %#v", delivery.Events[0])
	}
}

func TestProjectRecordProjectsHistoricalTurnBoundaryWithoutTaskTerminalTransport(t *testing.T) {
	t.Parallel()

	at := time.Unix(205, 0)
	record := controltaskstream.Record{
		Cursor: "cursor-boundary", Generation: "generation-1", Sequence: 4,
		Task: controltaskstream.TaskDescriptor{
			SessionID: "session-1", TaskID: "task-1", Handle: "zuri", Kind: task.KindSubagent,
			State: task.StateRunning, Running: true, CurrentTurnID: "turn-2",
			ParentTool: controltaskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "Spawn"},
		},
		Frame: &controltaskstream.Frame{
			TerminalID: "turn-1",
			UpdatedAt:  at,
			Event: &session.Event{
				ID: "boundary-turn-1", Type: session.EventTypeLifecycle, Visibility: session.VisibilityUIOnly,
				Time: at,
				Scope: &session.EventScope{
					TurnID: "turn-1", Source: "task_stream_turn_boundary",
					Participant: session.ParticipantRef{
						ID: "task-1", Kind: session.ParticipantKindSubagent, DelegationID: "task-1",
					},
				},
				Lifecycle: &session.EventLifecycle{Status: string(task.StateCompleted)},
			},
		},
	}

	projected := projectRecord(record)
	if len(projected) != 1 || projected[0].Kind != eventstream.KindLifecycle || projected[0].Lifecycle == nil {
		t.Fatalf("projectRecord() = %#v, want one lifecycle boundary", projected)
	}
	envelope := projected[0]
	if envelope.TurnID != "turn-1" || envelope.Lifecycle.State != eventstream.LifecycleStateCompleted ||
		envelope.Final || !envelope.OccurredAt.Equal(at) {
		t.Fatalf("historical Turn boundary = %#v", envelope)
	}
	if envelope.Scope != eventstream.ScopeSubagent || envelope.ScopeID != "task-1" ||
		envelope.ParentTool == nil || envelope.ParentTool.ToolCallID != "spawn-1" {
		t.Fatalf("historical Turn boundary identity = %#v", envelope)
	}
}

func TestProjectRecordMountsRunCommandOutputOnParentTerminal(t *testing.T) {
	t.Parallel()

	taskDescriptor := controltaskstream.TaskDescriptor{
		SessionID: "session-1", TaskID: "task-1", Handle: "command", Kind: task.KindCommand,
		State: task.StateRunning, Running: true, CurrentTurnID: "runtime-terminal-1",
		ParentTool: controltaskstream.ParentTool{ToolCallID: "command-1", ToolName: "RunCommand"},
	}
	output := projectRecord(controltaskstream.Record{
		Cursor: "cursor-1", Generation: "generation-1", Sequence: 1, Task: taskDescriptor,
		Frame: &controltaskstream.Frame{
			TerminalID: "runtime-terminal-1",
			Text:       "line\n", Running: true,
		},
	})
	if len(output) != 1 {
		t.Fatalf("output projection = %#v, want one Envelope", output)
	}
	outputMeta := eventstream.UpdateMeta(output[0].Update)
	if terminalOutput, ok := eventmeta.TerminalOutput(outputMeta); !ok ||
		terminalOutput.TerminalID != "command-1" ||
		terminalOutput.Data != "line\n" {
		t.Fatalf("terminal output = %#v, want parent command terminal", outputMeta)
	}

	exitCode := 0
	final := projectRecord(controltaskstream.Record{
		Cursor: "cursor-2", Generation: "generation-1", Sequence: 2, Task: taskDescriptor,
		Frame: &controltaskstream.Frame{
			TerminalID: "runtime-terminal-1",
			State:      "completed", Closed: true, ExitCode: &exitCode,
		},
	})
	if len(final) != 1 {
		t.Fatalf("final projection = %#v, want one Envelope", final)
	}
	finalMeta := eventstream.UpdateMeta(final[0].Update)
	if terminalExit, ok := eventmeta.TerminalExit(finalMeta); !ok ||
		terminalExit.TerminalID != "command-1" ||
		terminalExit.ExitCode == nil ||
		*terminalExit.ExitCode != 0 {
		t.Fatalf("terminal exit = %#v, want parent command terminal", finalMeta)
	}
}

func TestProjectRecordKeepsOneEnvelopePerCursorWhenEventCarriesUsage(t *testing.T) {
	record := controltaskstream.Record{
		Cursor: "cursor-1", Generation: "generation-1", Sequence: 1,
		Task: controltaskstream.TaskDescriptor{
			SessionID: "session-1", TaskID: "task-1", Handle: "zuri", Kind: task.KindSubagent,
			State: task.StateRunning, Running: true,
			ParentTool: controltaskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "Spawn"},
		},
		Frame: &controltaskstream.Frame{
			TerminalID: "turn-1",
			Running:    true,
			Event: &session.Event{
				ID: "child-event-1", Type: session.EventTypeAssistant,
				Meta: map[string]any{"usage": map[string]any{
					"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5,
				}},
				Scope: &session.EventScope{Participant: session.ParticipantRef{Kind: session.ParticipantKindSubagent}},
				Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage), MessageID: "message-1",
					Content: session.ProtocolTextContent("child answer"),
				}},
			},
		},
	}

	projected := projectRecord(record)
	if len(projected) != 1 || projected[0].Cursor != "cursor-1" {
		t.Fatalf("projectRecord() = %#v, want one Envelope for one resumable cursor", projected)
	}
	if eventstream.UpdateType(projected[0].Update) == eventstream.UpdateUsage {
		t.Fatalf("projectRecord() lost narrative in favor of sibling usage: %#v", projected[0])
	}
}

func TestProtocolSubscriptionClosesControlSubscriptionWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	inner := &closingControlTaskSubscription{
		deliveries: make(chan controltaskstream.Delivery),
		closed:     make(chan struct{}),
	}
	sub := newSubscription(ctx, inner)
	cancel()

	select {
	case <-inner.closed:
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not close the Control Task subscription")
	}
	select {
	case _, open := <-sub.Deliveries():
		if open {
			t.Fatal("protocol Task subscription remained open after context cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("protocol Task subscription did not terminate")
	}
}

type closingControlTaskSubscription struct {
	deliveries chan controltaskstream.Delivery
	closed     chan struct{}
	once       sync.Once
}

func (s *closingControlTaskSubscription) Deliveries() <-chan controltaskstream.Delivery {
	return s.deliveries
}
func (*closingControlTaskSubscription) Err() error { return nil }
func (s *closingControlTaskSubscription) Close() error {
	s.once.Do(func() {
		close(s.closed)
		close(s.deliveries)
	})
	return nil
}
