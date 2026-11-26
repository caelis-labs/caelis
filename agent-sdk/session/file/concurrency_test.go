package file

import (
	"context"
	"errors"
	"fmt"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestSessionInteractionsProgressWithConcurrentWriterAndReaders(t *testing.T) {
	// This stress test proves lock progress and durable logical readback; host
	// sync latency and crash-boundary classification are tested separately.
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	primary := newLogicalTestStore(t, Config{RootDir: root, SessionIDGenerator: func() string { return "active-session" }})
	active, err := primary.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", Workspace: session.WorkspaceRef{Key: "ws-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	fence, err := primary.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "runtime-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 4)
	latencies := make(chan time.Duration, 64)
	var wg sync.WaitGroup
	measure := func(operation func() error) error {
		started := time.Now()
		err := operation()
		latencies <- time.Since(started)
		return err
	}
	run := func(operation func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			opCtx, opCancel := context.WithTimeout(ctx, 10*time.Second)
			defer opCancel()
			errs <- operation(opCtx)
		}()
	}

	run(func(opCtx context.Context) error {
		service := newLogicalTestStore(t, Config{RootDir: root})
		for i := 0; i < 20; i++ {
			err := measure(func() error {
				_, appendErr := service.AppendEvent(opCtx, session.AppendEventRequest{
					SessionRef:    active.SessionRef,
					MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeSubagentCompletion),
					Event: &session.Event{
						Type: session.EventTypeLifecycle, Visibility: session.VisibilityCanonical,
						Lifecycle: &session.EventLifecycle{Status: "completed", Reason: fmt.Sprintf("concurrent-%02d", i)},
					},
				})
				return appendErr
			})
			if err != nil {
				return fmt.Errorf("append %d: %w", i, err)
			}
		}
		return nil
	})
	run(func(opCtx context.Context) error {
		service := newLogicalTestStore(t, Config{RootDir: root})
		for i := 0; i < 20; i++ {
			err := measure(func() error {
				_, pageErr := service.EventsPage(opCtx, session.EventPageRequest{
					SessionRef: active.SessionRef, Limit: 5, Visibility: session.EventPageAllDurable,
				})
				return pageErr
			})
			if err != nil {
				return fmt.Errorf("events page %d: %w", i, err)
			}
		}
		return nil
	})
	run(func(opCtx context.Context) error {
		service := newLogicalTestStore(t, Config{RootDir: root})
		err := measure(func() error {
			_, startErr := service.StartSession(opCtx, session.StartSessionRequest{
				AppName: "caelis", UserID: "user-1", PreferredSessionID: "new-session",
				Workspace: session.WorkspaceRef{Key: "ws-1"},
			})
			return startErr
		})
		if err != nil {
			return fmt.Errorf("new Session: %w", err)
		}
		return nil
	})
	run(func(opCtx context.Context) error {
		service := newLogicalTestStore(t, Config{RootDir: root})
		for i := 0; i < 5; i++ {
			err := measure(func() error {
				_, loadErr := service.LoadSession(opCtx, session.LoadSessionRequest{
					SessionRef: active.SessionRef, Limit: 1,
				})
				return loadErr
			})
			if err != nil {
				return fmt.Errorf("resume load %d: %w", i, err)
			}
		}
		return nil
	})

	close(start)
	wg.Wait()
	close(errs)
	close(latencies)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("concurrent interaction deadline: %v", err)
	}
	var measured []time.Duration
	for latency := range latencies {
		measured = append(measured, latency)
	}
	sort.Slice(measured, func(i, j int) bool { return measured[i] < measured[j] })
	percentile := func(percent int) time.Duration {
		index := (len(measured)*percent + 99) / 100
		index = max(1, index) - 1
		return measured[min(index, len(measured)-1)]
	}
	t.Logf("concurrent Store operation latency: n=%d p50=%s p95=%s p99=%s max=%s", len(measured), percentile(50), percentile(95), percentile(99), measured[len(measured)-1])

	reopened := newLogicalTestStore(t, Config{RootDir: root})
	list, err := reopened.ListSessions(context.Background(), session.ListSessionsRequest{
		AppName: "caelis", UserID: "user-1", WorkspaceKey: "ws-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 2 {
		t.Fatalf("Sessions after concurrent /new = %d, want 2", len(list.Sessions))
	}
	page, err := reopened.EventsPage(context.Background(), session.EventPageRequest{
		SessionRef: active.SessionRef, Visibility: session.EventPageAllDurable,
	})
	if err != nil || len(page.Events) != 20 {
		t.Fatalf("durable events after concurrent writes = %d, %v, want 20", len(page.Events), err)
	}
	durableFence, err := reopened.SessionFence(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if durableFence.FenceID != fence.FenceID || durableFence.FencingToken != fence.FencingToken {
		t.Fatalf("durable fence changed during concurrent interactions: got %#v, want %#v", durableFence, fence)
	}
}

func TestSessionStoreRootLockHonorsContextCancellation(t *testing.T) {
	root := t.TempDir()
	first, err := lockSessionStoreRoot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unlockSessionStoreRoot(first); err != nil {
			t.Errorf("unlock first session store root: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	second, err := lockSessionStoreRoot(ctx, root)
	if second != nil {
		_ = unlockSessionStoreRoot(second)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second lock error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("cancelled lock wait took %v", elapsed)
	}
}

func TestSessionStoreRootLockRejectsPreCancelledContextWhenFree(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	file, err := lockSessionStoreRoot(ctx, root)
	if file != nil {
		_ = unlockSessionStoreRoot(file)
		t.Fatal("pre-cancelled root lock returned an acquired file")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled root lock error = %v, want context cancellation", err)
	}
}

func TestContextMutexRejectsPreCancelledContextWhenFree(t *testing.T) {
	var mutex contextMutex
	for range 256 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := mutex.LockContext(ctx)
		if err == nil {
			mutex.Unlock()
			t.Fatal("pre-cancelled context acquired a free contextMutex")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pre-cancelled contextMutex error = %v, want context cancellation", err)
		}
	}
}

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
		"update_state": func(ctx context.Context) error {
			_, err := store.UpdateState(ctx, session.UpdateStateRequest{
				SessionRef: active.SessionRef,
				Update:     func(map[string]any) (map[string]any, error) { return map[string]any{"should": "cancel"}, nil },
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
			if err := store.mu.LockContext(t.Context()); err != nil {
				t.Fatal(err)
			}
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
	held, err := lockSessionStoreRoot(context.Background(), root)
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
