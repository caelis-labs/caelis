package file

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
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
					MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest),
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
