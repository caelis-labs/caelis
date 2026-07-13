package session_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func TestSessionLeaseServiceConformance(t *testing.T) {
	t.Parallel()

	for _, store := range []string{"memory", "file"} {
		store := store
		t.Run(store, func(t *testing.T) {
			t.Parallel()
			clock := &leaseTestClock{now: time.Unix(100, 0)}
			var service session.Service
			var reopen func() session.Service
			switch store {
			case "memory":
				base := inmemory.NewStore(inmemory.Config{Clock: clock.Now})
				service = inmemory.NewService(base)
				reopen = func() session.Service { return inmemory.NewService(base) }
			case "file":
				root := t.TempDir()
				service = sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root, Clock: clock.Now}))
				reopen = func() session.Service {
					return sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root, Clock: clock.Now}))
				}
			}
			ctx := context.Background()
			active, err := service.StartSession(ctx, session.StartSessionRequest{
				AppName: "caelis", UserID: "user-1", PreferredSessionID: "lease-session",
			})
			if err != nil {
				t.Fatal(err)
			}
			leases := service.(session.SessionLeaseService)
			first, err := leases.AcquireSessionLease(ctx, session.AcquireSessionLeaseRequest{
				SessionRef: active.SessionRef, OwnerID: "host-a", TTL: time.Minute,
			})
			if err != nil {
				t.Fatalf("AcquireSessionLease() error = %v", err)
			}
			if first.LeaseID == "" || first.Revision != 1 || first.OwnerID != "host-a" {
				t.Fatalf("first lease = %#v", first)
			}
			if first.FencingToken == 0 {
				t.Fatalf("first fencing token = %d, want positive", first.FencingToken)
			}
			if _, err := leases.AcquireSessionLease(ctx, session.AcquireSessionLeaseRequest{
				SessionRef: active.SessionRef, OwnerID: "host-a", TTL: time.Minute,
			}); !errors.Is(err, session.ErrLeaseConflict) {
				t.Fatalf("same-owner second acquisition error = %v, want ErrLeaseConflict", err)
			}

			reopened := reopen().(session.SessionLeaseService)
			if _, err := reopened.AcquireSessionLease(ctx, session.AcquireSessionLeaseRequest{
				SessionRef: active.SessionRef, OwnerID: "host-b", TTL: time.Minute,
			}); !errors.Is(err, session.ErrLeaseConflict) {
				t.Fatalf("competing acquire error = %v, want ErrLeaseConflict", err)
			}
			if _, err := reopened.HeartbeatSessionLease(ctx, session.HeartbeatSessionLeaseRequest{
				SessionRef: active.SessionRef, LeaseID: first.LeaseID, OwnerID: first.OwnerID,
				ExpectedLeaseRevision: first.Revision + 1, TTL: time.Minute,
			}); !errors.Is(err, session.ErrLeaseConflict) {
				t.Fatalf("stale heartbeat error = %v, want ErrLeaseConflict", err)
			}
			heartbeat, err := reopened.HeartbeatSessionLease(ctx, session.HeartbeatSessionLeaseRequest{
				SessionRef: active.SessionRef, LeaseID: first.LeaseID, OwnerID: first.OwnerID,
				ExpectedLeaseRevision: first.Revision, TTL: time.Minute,
			})
			if err != nil || heartbeat.Revision != 2 || heartbeat.FencingToken != first.FencingToken {
				t.Fatalf("HeartbeatSessionLease() = %#v, %v", heartbeat, err)
			}
			if err := leases.ReleaseSessionLease(ctx, session.ReleaseSessionLeaseRequest{
				SessionRef: active.SessionRef, LeaseID: heartbeat.LeaseID, OwnerID: heartbeat.OwnerID,
				ExpectedLeaseRevision: heartbeat.Revision,
			}); err != nil {
				t.Fatalf("ReleaseSessionLease() error = %v", err)
			}
			second, err := reopened.AcquireSessionLease(ctx, session.AcquireSessionLeaseRequest{
				SessionRef: active.SessionRef, OwnerID: "host-b", TTL: time.Minute,
			})
			if err != nil || second.OwnerID != "host-b" || second.FencingToken <= first.FencingToken {
				t.Fatalf("second acquire = %#v, %v", second, err)
			}
			clock.Advance(2 * time.Minute)
			takenOver, err := leases.AcquireSessionLease(ctx, session.AcquireSessionLeaseRequest{
				SessionRef: active.SessionRef, OwnerID: "host-c", TTL: time.Minute,
			})
			if err != nil || takenOver.OwnerID != "host-c" || takenOver.LeaseID == second.LeaseID || takenOver.FencingToken <= second.FencingToken {
				t.Fatalf("expired takeover = %#v, %v", takenOver, err)
			}

			staleGuard := session.MutationGuard{Authority: session.MutationAuthorityRuntime, LeaseID: second.LeaseID, OwnerID: second.OwnerID, FencingToken: second.FencingToken}
			assertLeaseFencedMutations(t, service, active.SessionRef, staleGuard, takenOver)
		})
	}
}

func TestSessionLeaseExpiryBoundaryConformance(t *testing.T) {
	t.Parallel()
	for _, store := range []string{"memory", "file"} {
		store := store
		t.Run(store, func(t *testing.T) {
			t.Parallel()
			clock := &leaseTestClock{now: time.Unix(1_000, 0)}
			var service session.Service
			switch store {
			case "memory":
				service = inmemory.NewService(inmemory.NewStore(inmemory.Config{Clock: clock.Now}))
			case "file":
				service = sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir(), Clock: clock.Now}))
			}
			active, err := service.StartSession(context.Background(), session.StartSessionRequest{
				AppName: "caelis", UserID: "user-1", PreferredSessionID: "lease-boundary",
			})
			if err != nil {
				t.Fatal(err)
			}
			leases := service.(session.SessionLeaseService)
			const ttl = time.Minute
			old, err := leases.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
				SessionRef: active.SessionRef, OwnerID: "old-owner", TTL: ttl,
			})
			if err != nil {
				t.Fatal(err)
			}
			clock.Advance(ttl)
			_, err = leases.HeartbeatSessionLease(context.Background(), session.HeartbeatSessionLeaseRequest{
				SessionRef: active.SessionRef, LeaseID: old.LeaseID, OwnerID: old.OwnerID,
				ExpectedLeaseRevision: old.Revision, TTL: ttl,
			})
			requireLeaseConflictDetail(t, err, "lease has expired")

			current, err := leases.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
				SessionRef: active.SessionRef, OwnerID: "new-owner", TTL: ttl,
			})
			if err != nil {
				t.Fatalf("takeover at exact expiry = %v", err)
			}
			err = leases.ReleaseSessionLease(context.Background(), session.ReleaseSessionLeaseRequest{
				SessionRef: active.SessionRef, LeaseID: old.LeaseID, OwnerID: old.OwnerID, ExpectedLeaseRevision: old.Revision,
			})
			requireLeaseConflictDetail(t, err, "lease identity, owner, or revision mismatch")

			message := model.NewTextMessage(model.RoleUser, "old owner")
			oldGuard := session.MutationGuard{
				Authority: session.MutationAuthorityRuntime, LeaseID: old.LeaseID,
				OwnerID: old.OwnerID, FencingToken: old.FencingToken,
			}
			_, err = service.AppendEvent(context.Background(), session.AppendEventRequest{
				SessionRef: active.SessionRef, MutationGuard: oldGuard,
				Event: &session.Event{Type: session.EventTypeUser, Message: &message},
			})
			requireLeaseConflictDetail(t, err, "runtime fencing token is stale")

			if err := leases.ReleaseSessionLease(context.Background(), session.ReleaseSessionLeaseRequest{
				SessionRef: active.SessionRef, LeaseID: current.LeaseID, OwnerID: current.OwnerID,
				ExpectedLeaseRevision: current.Revision,
			}); err != nil {
				t.Fatal(err)
			}
			_, err = service.AppendEvent(context.Background(), session.AppendEventRequest{
				SessionRef: active.SessionRef, MutationGuard: oldGuard,
				Event: &session.Event{Type: session.EventTypeUser, Message: &message},
			})
			requireLeaseConflictDetail(t, err, "runtime lease is absent or expired")
		})
	}
}

func assertLeaseFencedMutations(
	t *testing.T,
	service session.Service,
	ref session.SessionRef,
	stale session.MutationGuard,
	current session.SessionLease,
) {
	t.Helper()
	user := model.NewTextMessage(model.RoleUser, "stale append")
	if _, err := service.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: ref, MutationGuard: stale, Event: &session.Event{Type: session.EventTypeUser, Message: &user},
	}); !errors.Is(err, session.ErrLeaseConflict) {
		t.Fatalf("stale AppendEvent error = %v, want ErrLeaseConflict", err)
	}
	batch := service.(session.EventBatchService)
	if _, err := batch.AppendEvents(context.Background(), session.AppendEventsRequest{
		SessionRef: ref, MutationGuard: stale, Events: []*session.Event{{Type: session.EventTypeUser, Message: &user}},
	}); !errors.Is(err, session.ErrLeaseConflict) {
		t.Fatalf("stale AppendEvents error = %v, want ErrLeaseConflict", err)
	}
	compound := service.(session.EventBatchStateService)
	if _, err := compound.AppendEventsAndUpdateState(context.Background(), session.AppendEventsAndUpdateStateRequest{
		SessionRef: ref, MutationGuard: stale, TransactionID: "stale-compound",
		MutationDigest: "stale-compound-v1",
		Events:         []*session.Event{{Type: session.EventTypeUser, Message: &user}},
		UpdateState: func(_ []*session.Event, state map[string]any) (map[string]any, error) {
			state["stale"] = true
			return state, nil
		},
	}); !errors.Is(err, session.ErrLeaseConflict) {
		t.Fatalf("stale compound mutation error = %v, want ErrLeaseConflict", err)
	}
	if _, err := service.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: ref, Event: &session.Event{Type: session.EventTypeUser, Message: &user},
	}); !errors.Is(err, session.ErrLeaseConflict) {
		t.Fatalf("unscoped AppendEvent error = %v, want ErrLeaseConflict", err)
	}
	controlMessage := model.NewTextMessage(model.RoleUser, "control append")
	if _, err := service.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: ref, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest), Event: &session.Event{Type: session.EventTypeUser, Message: &controlMessage},
	}); err != nil {
		t.Fatalf("control AppendEvent error = %v", err)
	}
	if _, err := service.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: ref, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeApproval), Event: &session.Event{Type: session.EventTypeUser, Message: &controlMessage},
	}); err != nil {
		t.Fatalf("overlapping approval AppendEvent error = %v", err)
	}
	if _, err := service.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: ref, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeParticipant), Event: &session.Event{Type: session.EventTypeUser, Message: &controlMessage},
	}); err != nil {
		t.Fatalf("overlapping participant AppendEvent error = %v", err)
	}
	if _, err := service.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: ref, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeHandoff), Event: &session.Event{Type: session.EventTypeUser, Message: &controlMessage},
	}); !errors.Is(err, session.ErrLeaseConflict) {
		t.Fatalf("unfenced handoff AppendEvent error = %v, want ErrLeaseConflict", err)
	}
	for _, purpose := range []session.ControlMutationPurpose{
		session.ControlMutationPurposeLifecycle,
		session.ControlMutationPurposeConfiguration,
	} {
		if _, err := service.AppendEvent(context.Background(), session.AppendEventRequest{
			SessionRef: ref, MutationGuard: session.ControlMutationGuard(purpose), Event: &session.Event{Type: session.EventTypeUser, Message: &controlMessage},
		}); !errors.Is(err, session.ErrLeaseConflict) {
			t.Fatalf("overlapping %s AppendEvent error = %v, want ErrLeaseConflict", purpose, err)
		}
	}
	staleControl := session.MutationGuard{
		Authority: session.MutationAuthorityControl, Purpose: session.ControlMutationPurposeHandoff,
		LeaseID: stale.LeaseID, OwnerID: stale.OwnerID, FencingToken: stale.FencingToken,
	}
	if _, err := service.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: ref, MutationGuard: staleControl, Event: &session.Event{Type: session.EventTypeUser, Message: &controlMessage},
	}); !errors.Is(err, session.ErrLeaseConflict) {
		t.Fatalf("stale fenced handoff AppendEvent error = %v, want ErrLeaseConflict", err)
	}
	placedCtx := session.ContextWithRuntimeLease(context.Background(), current)
	currentSession, err := service.Session(context.Background(), ref)
	if err != nil {
		t.Fatalf("Session(before state fencing) error = %v", err)
	}
	if _, err := service.UpdateState(context.Background(), session.UpdateStateRequest{
		SessionRef: ref, ExpectedRevision: &currentSession.Revision, MutationGuard: stale,
		Update: func(state map[string]any) (map[string]any, error) {
			state["stale"] = true
			return state, nil
		},
	}); !errors.Is(err, session.ErrLeaseConflict) {
		t.Fatalf("stale UpdateState fence error = %v, want ErrLeaseConflict", err)
	}
	if _, err := service.ReplaceState(context.Background(), session.ReplaceStateRequest{
		SessionRef: ref, ExpectedRevision: &currentSession.Revision, State: map[string]any{"unscoped": true},
	}); !errors.Is(err, session.ErrLeaseConflict) {
		t.Fatalf("unscoped ReplaceState error = %v, want ErrLeaseConflict", err)
	}
	controlSession, err := service.UpdateState(context.Background(), session.UpdateStateRequest{
		SessionRef: ref, ExpectedRevision: &currentSession.Revision,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeApproval),
		Update: func(state map[string]any) (map[string]any, error) {
			state["approval"] = true
			return state, nil
		},
	})
	if err != nil {
		t.Fatalf("overlapping approval UpdateState error = %v", err)
	}
	runtimeSession, err := service.ReplaceState(context.Background(), session.ReplaceStateRequest{
		SessionRef: ref, ExpectedRevision: &controlSession.Revision,
		MutationGuard: session.RuntimeMutationGuard(placedCtx), State: map[string]any{"runtime": true},
	})
	if err != nil {
		t.Fatalf("matching runtime ReplaceState error = %v", err)
	}
	staleRevision := controlSession.Revision
	if _, err := service.UpdateState(context.Background(), session.UpdateStateRequest{
		SessionRef: ref, ExpectedRevision: &staleRevision, MutationGuard: session.RuntimeMutationGuard(placedCtx),
		Update: func(state map[string]any) (map[string]any, error) { return state, nil },
	}); !errors.Is(err, session.ErrRevisionConflict) {
		t.Fatalf("stale UpdateState revision error = %v, want ErrRevisionConflict", err)
	}
	if runtimeSession.Revision != controlSession.Revision+1 {
		t.Fatalf("runtime state revision = %d, want %d", runtimeSession.Revision, controlSession.Revision+1)
	}
	if _, err := service.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef:    ref,
		MutationGuard: session.ControlMutationGuardWithRuntimeLease(placedCtx, session.ControlMutationPurposeHandoff),
		Event:         &session.Event{Type: session.EventTypeUser, Message: &controlMessage},
	}); err != nil {
		t.Fatalf("matching fenced handoff AppendEvent error = %v", err)
	}
	if err := service.(session.SessionLeaseService).ReleaseSessionLease(context.Background(), session.ReleaseSessionLeaseRequest{
		SessionRef: ref, LeaseID: current.LeaseID, OwnerID: current.OwnerID, ExpectedLeaseRevision: current.Revision,
	}); err != nil {
		t.Fatalf("ReleaseSessionLease(current) error = %v", err)
	}
	if _, err := service.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef:    ref,
		MutationGuard: session.ControlMutationGuardWithRuntimeLease(placedCtx, session.ControlMutationPurposeHandoff),
		Event:         &session.Event{Type: session.EventTypeUser, Message: &controlMessage},
	}); !errors.Is(err, session.ErrLeaseConflict) {
		t.Fatalf("late fenced handoff AppendEvent error = %v, want ErrLeaseConflict", err)
	}
	if _, err := service.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef:    ref,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeHandoff),
		Event:         &session.Event{Type: session.EventTypeUser, Message: &controlMessage},
	}); !errors.Is(err, session.ErrLeaseConflict) {
		t.Fatalf("quiescent unfenced handoff AppendEvent error = %v, want ErrLeaseConflict", err)
	}
	if _, err := service.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef:    ref,
		MutationGuard: session.ControlMutationGuard("future_unknown"),
		Event:         &session.Event{Type: session.EventTypeUser, Message: &controlMessage},
	}); !errors.Is(err, session.ErrLeaseConflict) {
		t.Fatalf("unknown control purpose AppendEvent error = %v, want ErrLeaseConflict", err)
	}
	if _, err := service.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef:    ref,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeConfiguration),
		Event:         &session.Event{Type: session.EventTypeUser, Message: &controlMessage},
	}); err != nil {
		t.Fatalf("quiescent configuration AppendEvent error = %v", err)
	}

	leases := service.(session.SessionLeaseService)
	fresh, err := leases.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
		SessionRef: ref, OwnerID: current.OwnerID, TTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("same-owner reacquire error = %v", err)
	}
	if fresh.LeaseID == current.LeaseID || fresh.FencingToken <= current.FencingToken {
		t.Fatalf("same-owner fresh lease = %#v, want distinct LeaseID and increasing fence after %#v", fresh, current)
	}
	if err := leases.ReleaseSessionLease(context.Background(), session.ReleaseSessionLeaseRequest{
		SessionRef: ref, LeaseID: fresh.LeaseID, OwnerID: fresh.OwnerID, ExpectedLeaseRevision: fresh.Revision,
	}); err != nil {
		t.Fatalf("release same-owner fresh lease error = %v", err)
	}
}

func requireLeaseConflictDetail(t *testing.T, err error, detail string) {
	t.Helper()
	var conflict *session.LeaseConflictError
	if !errors.As(err, &conflict) || !strings.Contains(conflict.Detail, detail) {
		t.Fatalf("lease conflict = %v, want detail containing %q", err, detail)
	}
}

type leaseTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *leaseTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *leaseTestClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}
