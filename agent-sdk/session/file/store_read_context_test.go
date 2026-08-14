package file

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestStoreReadPathsHonorContextWhileWaitingForStoreMutex(t *testing.T) {
	store := NewStore(Config{RootDir: t.TempDir(), SessionIDGenerator: func() string { return "session-1" }})
	ctx := context.Background()
	active, err := store.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", Workspace: session.WorkspaceRef{Key: "ws-1", CWD: "/tmp/ws-1"},
	})

	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{
		Type:       session.EventTypeLifecycle,
		Visibility: session.VisibilityCanonical,
		Lifecycle:  &session.EventLifecycle{Status: "completed", Reason: "fixture"},
	}}); err != nil {
		t.Fatal(err)
	}
	tasks := NewTaskStore(store)

	operations := map[string]func(context.Context) error{
		"get_or_create": func(ctx context.Context) error {
			_, err := store.StartSession(ctx, session.StartSessionRequest{
				AppName: "caelis", UserID: "user-1", PreferredSessionID: "session-2",
			})

			return err
		},
		"get": func(ctx context.Context) error {
			_, err := store.Session(ctx, active.SessionRef)
			return err
		},
		"list": func(ctx context.Context) error {
			_, err := store.ListSessions(ctx, session.ListSessionsRequest{AppName: "caelis", UserID: "user-1"})
			return err
		},
		"events": func(ctx context.Context) error {
			_, err := store.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef})
			return err
		},
		"events_page": func(ctx context.Context) error {
			_, err := store.EventsPage(ctx, session.EventPageRequest{SessionRef: active.SessionRef})
			return err
		},
		"snapshot_state": func(ctx context.Context) error {
			_, err := store.SnapshotState(ctx, active.SessionRef)
			return err
		},
		"load_document": func(ctx context.Context) error {
			_, err := store.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
			return err
		},
		"append_event": func(ctx context.Context) error {
			_, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{
				Type:       session.EventTypeLifecycle,
				Visibility: session.VisibilityCanonical,
				Lifecycle:  &session.EventLifecycle{Status: "completed", Reason: "should-cancel"},
			}})

			return err
		},
		"append_events": func(ctx context.Context) error {
			_, err := store.AppendEvents(ctx, session.AppendEventsRequest{
				SessionRef: active.SessionRef,
				Events: []*session.Event{{
					Type:       session.EventTypeLifecycle,
					Visibility: session.VisibilityCanonical,
					Lifecycle:  &session.EventLifecycle{Status: "completed", Reason: "batch-should-cancel"},
				}},
			})
			return err
		},
		"append_events_and_state": func(ctx context.Context) error {
			_, err := store.AppendEventsAndUpdateState(ctx, session.AppendEventsAndUpdateStateRequest{
				SessionRef:     active.SessionRef,
				TransactionID:  "cancelled-batch-state",
				MutationDigest: "cancelled-batch-state-v1",
				UpdateState: func(_ []*session.Event, state map[string]any) (map[string]any, error) {
					return state, nil
				},
			})
			return err
		},
		"pending_approvals": func(ctx context.Context) error {
			_, err := store.PendingApprovals(ctx)
			return err
		},
		"put_participant_with_event": func(ctx context.Context) error {
			_, _, err := store.PutParticipantWithEvent(ctx, session.PutParticipantWithEventRequest{
				SessionRef: active.SessionRef,
				Binding:    session.ParticipantBinding{ID: "cancelled-participant"},
				Event: &session.Event{
					Type:       session.EventTypeLifecycle,
					Visibility: session.VisibilityCanonical,
					Lifecycle:  &session.EventLifecycle{Status: "completed", Reason: "participant-should-cancel"},
				},
			})
			return err
		},
		"replace_state": func(ctx context.Context) error {
			_, err := store.ReplaceState(ctx, session.ReplaceStateRequest{
				SessionRef: active.SessionRef,
				State:      map[string]any{"should": "cancel"},
			})
			return err
		},
		"task_get": func(ctx context.Context) error {
			_, err := tasks.Get(ctx, "cancelled-task")
			return err
		},
	}

	for name, operation := range operations {
		operation := operation
		t.Run(name, func(t *testing.T) {
			store.mu.Lock()
			callCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- operation(callCtx) }()
			<-callCtx.Done()
			store.mu.Unlock()
			select {
			case err := <-result:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("operation error = %v, want context deadline exceeded", err)
				}
			case <-time.After(time.Second):
				t.Fatal("operation did not return after Store mutex release")
			}
		})
	}
}

func TestStoreListHonorsContextWhileWaitingForRootFileLock(t *testing.T) {
	root := t.TempDir()
	store := NewStore(Config{RootDir: root})
	if _, err := store.ListSessions(context.Background(), session.ListSessionsRequest{}); err != nil {
		t.Fatal(err)
	}
	held, err := lockSessionStoreRoot(context.Background(), root, storeRootLockExclusive)
	if err != nil {
		t.Fatal(err)
	}

	callCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := store.ListSessions(callCtx, session.ListSessionsRequest{})
		result <- err
	}()
	<-callCtx.Done()
	if err := unlockSessionStoreRoot(held); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("List() error = %v, want context deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("List() did not return after root lock release")
	}
}
