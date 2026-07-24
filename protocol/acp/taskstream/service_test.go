package taskstream

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	sdkstream "github.com/caelis-labs/caelis/agent-sdk/task/stream"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

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
			Frame: &sdkstream.Frame{
				Ref:     sdkstream.Ref{SessionID: "session-1", TaskID: taskID, TerminalID: "shared-turn"},
				Running: true, Cursor: sdkstream.Cursor{Events: 1},
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
			State: task.StateCompleted, CurrentTurnID: "turn-2",
			ParentTool: controltaskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "Spawn"},
		},
		Frame: &sdkstream.Frame{
			Ref:   sdkstream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-2"},
			State: string(task.StateCompleted), Closed: true, Cursor: sdkstream.Cursor{Events: 9},
		},
	}

	projected := projectRecord(record)
	if len(projected) != 1 {
		t.Fatalf("projectRecord() = %#v, want one lifecycle envelope", projected)
	}
	envelope := projected[0]
	if envelope.Cursor != "cursor-7" || envelope.Scope != eventstream.ScopeSubagent || envelope.ScopeID != "task-1" {
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

func TestProjectRecordMountsRunCommandOutputOnParentTerminal(t *testing.T) {
	t.Parallel()

	taskDescriptor := controltaskstream.TaskDescriptor{
		SessionID: "session-1", TaskID: "task-1", Handle: "command", Kind: task.KindCommand,
		State: task.StateRunning, Running: true, CurrentTurnID: "runtime-terminal-1",
		ParentTool: controltaskstream.ParentTool{ToolCallID: "command-1", ToolName: "RunCommand"},
	}
	output := projectRecord(controltaskstream.Record{
		Cursor: "cursor-1", Generation: "generation-1", Sequence: 1, Task: taskDescriptor,
		Frame: &sdkstream.Frame{
			Ref: sdkstream.Ref{
				SessionID: "session-1", TaskID: "task-1", TerminalID: "runtime-terminal-1",
			},
			Text: "line\n", Running: true, Cursor: sdkstream.Cursor{Output: 5},
		},
	})
	if len(output) != 1 {
		t.Fatalf("output projection = %#v, want one Envelope", output)
	}
	outputMeta := eventstream.UpdateMeta(output[0].Update)
	if terminalOutput, ok := metautil.TerminalOutput(outputMeta); !ok ||
		terminalOutput.TerminalID != "command-1" ||
		terminalOutput.Data != "line\n" {
		t.Fatalf("terminal output = %#v, want parent command terminal", outputMeta)
	}

	exitCode := 0
	final := projectRecord(controltaskstream.Record{
		Cursor: "cursor-2", Generation: "generation-1", Sequence: 2, Task: taskDescriptor,
		Frame: &sdkstream.Frame{
			Ref: sdkstream.Ref{
				SessionID: "session-1", TaskID: "task-1", TerminalID: "runtime-terminal-1",
			},
			State: "completed", Closed: true, ExitCode: &exitCode, Cursor: sdkstream.Cursor{Output: 5},
		},
	})
	if len(final) != 1 {
		t.Fatalf("final projection = %#v, want one Envelope", final)
	}
	finalMeta := eventstream.UpdateMeta(final[0].Update)
	if terminalExit, ok := metautil.TerminalExit(finalMeta); !ok ||
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
			ParentTool: controltaskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "SPAWN"},
		},
		Frame: &sdkstream.Frame{
			Ref:    sdkstream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "turn-1"},
			Cursor: sdkstream.Cursor{Events: 1}, Running: true,
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
	if eventstream.UpdateType(projected[0].Update) == schema.UpdateUsage {
		t.Fatalf("projectRecord() lost narrative in favor of sibling usage: %#v", projected[0])
	}
}

func TestProjectRecordMakesGapExplicitAndDropsEmptyOversizeMarker(t *testing.T) {
	descriptor := controltaskstream.TaskDescriptor{
		SessionID: "session-1", TaskID: "task-1", Handle: "command", Kind: task.KindCommand, State: task.StateRunning,
	}
	gap := controltaskstream.Record{
		Cursor: "cursor-1", Generation: "generation-1", Sequence: 1, Task: descriptor,
		Gap: &controltaskstream.Gap{SessionID: "session-1", TaskID: "task-1", Kind: task.KindCommand, State: task.StateRunning},
	}
	projected := projectRecord(gap)
	if len(projected) != 1 || projected[0].Kind != eventstream.KindNotice || projected[0].Cursor != "cursor-1" {
		t.Fatalf("gap projection = %#v", projected)
	}

	marker := controltaskstream.Record{
		Cursor: "cursor-2", Generation: "generation-1", Sequence: 2, Task: descriptor,
		Frame: &sdkstream.Frame{
			Ref:     sdkstream.Ref{SessionID: "session-1", TaskID: "task-1"},
			Running: true, Cursor: sdkstream.Cursor{Events: 1, Output: 5 * 1024 * 1024},
		},
	}
	if projected := projectRecord(marker); len(projected) != 0 {
		t.Fatalf("oversize marker projection = %#v, want no body envelope", projected)
	}
}

func TestProtocolSubscriptionClosesControlSubscriptionWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	inner := &closingControlTaskSubscription{
		records: make(chan controltaskstream.Record),
		closed:  make(chan struct{}),
	}
	sub := newSubscription(ctx, inner)
	cancel()

	select {
	case <-inner.closed:
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not close the Control Task subscription")
	}
	select {
	case _, open := <-sub.Events():
		if open {
			t.Fatal("protocol Task subscription remained open after context cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("protocol Task subscription did not terminate")
	}
}

type closingControlTaskSubscription struct {
	records chan controltaskstream.Record
	closed  chan struct{}
	once    sync.Once
}

func (s *closingControlTaskSubscription) Records() <-chan controltaskstream.Record { return s.records }
func (*closingControlTaskSubscription) Err() error                                 { return nil }
func (*closingControlTaskSubscription) LastCursor() string                         { return "" }
func (s *closingControlTaskSubscription) Close() error {
	s.once.Do(func() {
		close(s.closed)
		close(s.records)
	})
	return nil
}
