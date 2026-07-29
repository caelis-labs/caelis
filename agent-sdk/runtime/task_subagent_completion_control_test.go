package runtime

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
)

func TestCanonicalSubagentSyncIsTurnMonotonic(t *testing.T) {
	tests := []struct {
		name            string
		durableState    taskapi.State
		durableRunning  bool
		durableResult   map[string]any
		incomingState   taskapi.State
		incomingTurnSeq int64
		expectedTurnSeq int64
	}{
		{
			name:            "terminal rejects running same turn",
			durableState:    taskapi.StateCompleted,
			durableResult:   map[string]any{"state": string(taskapi.StateCompleted), "final_message": "terminal truth"},
			incomingState:   taskapi.StateRunning,
			incomingTurnSeq: 2,
			expectedTurnSeq: 2,
		},
		{
			name:            "newer running rejects older terminal",
			durableState:    taskapi.StateRunning,
			durableRunning:  true,
			durableResult:   map[string]any{"state": string(taskapi.StateRunning), "output_preview": "newer turn"},
			incomingState:   taskapi.StateCompleted,
			incomingTurnSeq: 1,
			expectedTurnSeq: 2,
		},
	}
	for testIndex, test := range tests {
		for _, backfill := range []bool{false, true} {
			name := test.name + "/live"
			if backfill {
				name = test.name + "/backfill"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				runtime, active := newSubagentTaskTestRuntime(t, &recordingSubagentRunner{})
				taskID := fmt.Sprintf("monotonic-%d-%t", testIndex, backfill)
				eventTime := time.Now()
				entry := &taskapi.Entry{
					TaskID:    taskID,
					Handle:    taskID,
					Kind:      taskapi.KindSubagent,
					Session:   active.SessionRef,
					State:     test.durableState,
					Running:   test.durableRunning,
					UpdatedAt: eventTime.Add(-time.Minute),
					Spec: map[string]any{
						"turn_seq": test.expectedTurnSeq,
					},
					Result: test.durableResult,
					Metadata: map[string]any{
						"state":    string(test.durableState),
						"running":  test.durableRunning,
						"turn_seq": test.expectedTurnSeq,
					},
				}
				if err := runtime.tasks.store.Upsert(ctx, entry); err != nil {
					t.Fatalf("Upsert() error = %v", err)
				}
				before, err := runtime.tasks.store.Get(ctx, taskID)
				if err != nil {
					t.Fatalf("Get(before) error = %v", err)
				}
				event := &session.Event{
					Type: session.EventTypeToolResult,
					Time: eventTime,
					Meta: taskToolMeta(taskapi.Snapshot{
						Ref:      taskapi.Ref{TaskID: taskID},
						Handle:   taskID,
						Kind:     taskapi.KindSubagent,
						Metadata: map[string]any{"turn_seq": test.incomingTurnSeq},
					}),
					Tool: &session.EventTool{
						Name:   "TASK",
						Status: "completed",
						Output: map[string]any{
							"handle":         taskID,
							"state":          string(test.incomingState),
							"turn_seq":       test.incomingTurnSeq,
							"final_message":  "stale terminal",
							"output_preview": "stale running",
						},
					},
				}
				if backfill {
					if _, err := runtime.sessions.AppendEvent(ctx, session.AppendEventRequest{
						SessionRef: active.SessionRef,
						Event:      event,
					}); err != nil {
						t.Fatalf("AppendEvent() error = %v", err)
					}
					if _, err := runtime.tasks.backfillCanonicalTaskEntry(ctx, active.SessionRef, before); err != nil {
						t.Fatalf("backfillCanonicalTaskEntry() error = %v", err)
					}
				} else if err := runtime.tasks.syncCanonicalToolResult(ctx, active.SessionRef, event); err != nil {
					t.Fatalf("syncCanonicalToolResult() error = %v", err)
				}
				after, err := runtime.tasks.store.Get(ctx, taskID)
				if err != nil {
					t.Fatalf("Get(after) error = %v", err)
				}
				if after.Revision != before.Revision || after.State != before.State ||
					after.Running != before.Running || !reflect.DeepEqual(after.Result, before.Result) {
					t.Fatalf("canonical stale result mutated durable Task:\nbefore=%#v\nafter=%#v", before, after)
				}
			})
		}
	}
}

func TestDelayedCompletionCannotBeReopenedByWriteRunningResult(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{
			State: delegation.StateCompleted, Result: "first done",
		},
		continueResult: delegation.Result{
			State: delegation.StateRunning, Running: true, OutputPreview: "continuing",
		},
	}
	runtime, active := newSubagentTaskTestRuntime(t, runner)
	started, err := runtime.tasks.StartSubagent(ctx, active, active.SessionRef, runner, taskapi.SubagentStartRequest{
		Agent: "helper", Prompt: "first",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	running, err := runtime.tasks.Write(ctx, active.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Input: "follow up", Principal: session.ActorKindTool,
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if !running.Running || running.State != taskapi.StateRunning ||
		taskStringValue(running.Metadata["continue_phase"]) != string(continuePhasePostEffect) {
		t.Fatalf("Write() = %#v, want running post_effect before delayed completion", running)
	}
	staleRunningEvent := &session.Event{
		Type: session.EventTypeToolResult,
		Time: time.Now(),
		Meta: taskToolMeta(running),
		Tool: &session.EventTool{
			Name: "TASK", Status: "completed", Output: taskToolPayload(running),
		},
	}
	if runner.continueCompletion == nil {
		t.Fatal("Runtime-managed Continue installed no completion sink")
	}
	runner.continueCompletion.PublishSubagentCompletion(delegation.Result{
		TaskID: started.Ref.TaskID, State: delegation.StateCompleted, Result: "producer terminal",
	})
	terminal, err := runtime.tasks.store.Get(ctx, started.Ref.TaskID)
	if err != nil {
		t.Fatalf("Get(terminal) error = %v", err)
	}
	if terminal.State != taskapi.StateCompleted || terminal.Running ||
		taskStringValue(runtime.tasks.rehydrateSubagentTask(terminal).snapshot().Result["final_message"]) != "producer terminal" {
		t.Fatalf("producer terminal = %#v", terminal)
	}
	if metadataRunning, _ := terminal.Metadata["running"].(bool); metadataRunning {
		t.Fatalf("producer terminal retained running metadata: %#v", terminal.Metadata)
	}
	terminalRevision := terminal.Revision

	if err := runtime.tasks.syncCanonicalToolResult(ctx, active.SessionRef, staleRunningEvent); err != nil {
		t.Fatalf("syncCanonicalToolResult(stale running) error = %v", err)
	}
	after, err := runtime.tasks.store.Get(ctx, started.Ref.TaskID)
	if err != nil {
		t.Fatalf("Get(after stale sync) error = %v", err)
	}
	if after.Revision != terminalRevision || after.State != taskapi.StateCompleted || after.Running ||
		taskStringValue(runtime.tasks.rehydrateSubagentTask(after).snapshot().Result["final_message"]) != "producer terminal" {
		t.Fatalf("stale Running canonical result reopened producer terminal: %#v", after)
	}
}
