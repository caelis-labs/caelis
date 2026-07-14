package file

import (
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestPreCancelledAppendAndStateWriterDoNotCommitWithFreeLocks(t *testing.T) {
	service := NewService(NewStore(Config{RootDir: t.TempDir()}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "cancel-fence",
	})
	if err != nil {
		t.Fatal(err)
	}

	for range 256 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		appended, appendErr := service.AppendEvent(ctx, session.AppendEventRequest{
			SessionRef:    active.SessionRef,
			MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeConfiguration),
			Event: &session.Event{
				Type:       session.EventTypeLifecycle,
				Visibility: session.VisibilityCanonical,
				Lifecycle:  &session.EventLifecycle{Status: "completed", Reason: "pre-cancelled-append"},
			},
		})
		if appended != nil {
			t.Fatalf("pre-cancelled append returned event %#v", appended)
		}
		if !errors.Is(appendErr, context.Canceled) {
			t.Fatalf("pre-cancelled append error = %v, want context cancellation", appendErr)
		}

		updateCalled := false
		_, updateErr := service.UpdateState(ctx, session.UpdateStateRequest{
			SessionRef:       active.SessionRef,
			ExpectedRevision: &active.Revision,
			MutationGuard:    session.ControlMutationGuard(session.ControlMutationPurposeConfiguration),
			Update: func(state map[string]any) (map[string]any, error) {
				updateCalled = true
				state["pre_cancelled"] = true
				return state, nil
			},
		})
		if updateCalled {
			t.Fatal("pre-cancelled StateWriter invoked its mutation callback")
		}
		if !errors.Is(updateErr, context.Canceled) {
			t.Fatalf("pre-cancelled StateWriter error = %v, want context cancellation", updateErr)
		}
	}

	events, err := service.Events(context.Background(), session.EventsRequest{
		SessionRef: active.SessionRef, IncludeTransient: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("durable events after pre-cancelled appends = %#v, want none", events)
	}
	state, err := service.SnapshotState(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if len(state) != 0 {
		t.Fatalf("durable state after pre-cancelled writes = %#v, want empty", state)
	}
	current, err := service.Session(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != active.Revision {
		t.Fatalf("Session revision after pre-cancelled writes = %d, want %d", current.Revision, active.Revision)
	}
}
