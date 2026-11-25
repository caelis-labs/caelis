package runtime

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	memory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

func TestRuntimeRecoveryRetiresContinueWithoutReplayingInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		phase       string
		state       taskapi.State
		running     bool
		wantState   taskapi.State
		wantMessage string
	}{
		{name: "prepared", phase: "continue_prepared", state: taskapi.StateCompleted, wantState: taskapi.StateUnknownOutcome, wantMessage: "participant outcome could not be confirmed"},
		{name: "pending terminal-shaped", phase: "continue_pending", state: taskapi.StateCompleted, wantState: taskapi.StateUnknownOutcome, wantMessage: "participant outcome could not be confirmed"},
		{name: "unknown", phase: "continue_unknown_outcome", state: taskapi.StateUnknownOutcome, wantState: taskapi.StateUnknownOutcome, wantMessage: "participant outcome could not be confirmed"},
		{name: "post effect terminal", phase: "continue_post_effect", state: taskapi.StateCompleted, wantState: taskapi.StateCompleted},
		{name: "post effect running", phase: "continue_post_effect", state: taskapi.StateRunning, running: true, wantState: taskapi.StateUnknownOutcome, wantMessage: "participant outcome could not be confirmed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			sessions := memory.NewStore(memory.Config{})
			active, err := sessions.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "legacy-continue"})
			if err != nil {
				t.Fatal(err)
			}
			store := newSagaTaskStore()
			entry := &taskapi.Entry{
				TaskID: "legacy-" + tt.name, Kind: taskapi.KindSubagent, Session: active.SessionRef,
				State: tt.state, Running: tt.running, SupportsInput: true, SupportsCancel: true,
				Spec: map[string]any{
					"continue_phase": tt.phase, "turn_seq": int64(2), "agent": "helper", "agent_id": "child-1", "handle": "orbit",
				},
				Metadata: map[string]any{
					"continue_phase": tt.phase, "continue_prompt": "old follow-up", "participant_role": string(session.ParticipantRoleSidecar),
				},
				Result: map[string]any{
					"state": string(tt.state), "result": "durable remote result", "final_message": "durable remote result",
				},
			}
			if _, err := store.Put(ctx, taskapi.PutRequest{Entry: entry}); err != nil {
				t.Fatal(err)
			}
			runtime, err := New(testConfigWithACPForwarder(Config{
				Sessions: sessions, TaskStore: store, AgentFactory: chat.Factory{},
				ControllerContextRouter: testContextRouter{sessions: sessions},
			}))
			if err != nil {
				t.Fatal(err)
			}
			if err := runtime.recoverRuntimeState(ctx, active.SessionRef); err != nil {
				t.Fatal(err)
			}
			got, err := store.Get(ctx, entry.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != tt.wantState || got.Running || got.SupportsInput {
				t.Fatalf("recovered entry = %#v, want terminal %q without Task input", got, tt.wantState)
			}
			if got.Metadata["continue_phase"] != nil || got.Spec["continue_phase"] != nil {
				t.Fatalf("legacy markers survived recovery: %#v / %#v", got.Metadata, got.Spec)
			}
			if gotMessage := taskStringValue(got.Result["error"]); gotMessage != tt.wantMessage {
				t.Fatalf("error = %q, want %q", gotMessage, tt.wantMessage)
			}
			if tt.wantState == taskapi.StateCompleted && got.Result["final_message"] != "durable remote result" {
				t.Fatalf("lost confirmed result: %#v", got.Result)
			}
			events, err := sessions.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef})
			if err != nil || len(events) != 0 {
				t.Fatalf("retired input produced events = %#v, %v", events, err)
			}
			beforeRevision := got.Revision
			if err := runtime.recoverRuntimeState(ctx, active.SessionRef); err != nil {
				t.Fatal(err)
			}
			again, err := store.Get(ctx, entry.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if again.Revision != beforeRevision {
				t.Fatalf("second recovery revision = %d, want stable %d", again.Revision, beforeRevision)
			}
		})
	}
}
