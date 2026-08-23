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

func allowPriorHostFence(context.Context) (func(), bool) { return func() {}, true }

func TestPriorHostFenceReplacementRequiresStoreAuthorization(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		new  func(*testing.T) session.Service
	}{
		{name: "memory", new: func(*testing.T) session.Service { return inmemory.NewStore(inmemory.Config{}) }},
		{name: "file", new: func(t *testing.T) session.Service {
			return sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			service := tc.new(t)
			active, err := service.StartSession(context.Background(), session.StartSessionRequest{
				AppName: "caelis", UserID: "user-1", PreferredSessionID: "unauthorized-takeover",
			})
			if err != nil {
				t.Fatal(err)
			}
			fences := service.(session.SessionFenceService)
			if _, err := fences.AcquireSessionFence(context.Background(), session.AcquireSessionFenceRequest{
				SessionRef: active.SessionRef, OwnerID: "host-a",
			}); err != nil {
				t.Fatal(err)
			}
			if _, ok := service.(session.PriorHostFenceService); ok {
				t.Fatalf("ordinary %s Store unexpectedly exposes prior-Host replacement", tc.name)
			}
		})
	}
}

func TestPriorHostFenceCapabilityRechecksAuthorization(t *testing.T) {
	t.Parallel()
	for _, store := range []string{"memory", "file"} {
		store := store
		t.Run(store, func(t *testing.T) {
			t.Parallel()
			deny := func(context.Context) (func(), bool) { return nil, false }
			var service session.Service
			var priorHostFences session.PriorHostFenceService
			switch store {
			case "memory":
				service, priorHostFences = inmemory.NewStoreWithPriorHostFences(inmemory.Config{}, deny)
			case "file":
				service, priorHostFences = sessionfile.NewStoreWithPriorHostFences(sessionfile.Config{RootDir: t.TempDir()}, deny)
			}
			active, err := service.StartSession(context.Background(), session.StartSessionRequest{
				AppName: "caelis", UserID: "user-1", PreferredSessionID: "denied-takeover",
			})
			if err != nil {
				t.Fatal(err)
			}
			first, err := service.(session.SessionFenceService).AcquireSessionFence(context.Background(), session.AcquireSessionFenceRequest{
				SessionRef: active.SessionRef, OwnerID: "host-a",
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = priorHostFences.ReplacePriorHostSessionFence(context.Background(), session.AcquireSessionFenceRequest{
				SessionRef: active.SessionRef, OwnerID: "host-b",
			})
			if !errors.Is(err, session.ErrFenceConflict) || !strings.Contains(err.Error(), "ownership is not held") {
				t.Fatalf("denied prior-Host replacement error = %v, want Host-ownership conflict", err)
			}
			durable, err := service.(session.SessionFenceReader).SessionFence(context.Background(), active.SessionRef)
			if err != nil || durable.FenceID != first.FenceID || durable.OwnerID != first.OwnerID {
				t.Fatalf("fence after denied takeover = %#v, %v; want %#v", durable, err, first)
			}
		})
	}
}

func TestSessionFenceObservationCannotForgeReleaseOrMutation(t *testing.T) {
	t.Parallel()
	for _, store := range []string{"memory", "file"} {
		store := store
		t.Run(store, func(t *testing.T) {
			t.Parallel()
			var service session.Service
			switch store {
			case "memory":
				service = inmemory.NewStore(inmemory.Config{})
			case "file":
				service = sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
			}
			ctx := context.Background()
			active, err := service.StartSession(ctx, session.StartSessionRequest{
				AppName: "caelis", UserID: "user-1", PreferredSessionID: "unforgeable-fence",
			})
			if err != nil {
				t.Fatal(err)
			}
			fences := service.(session.SessionFenceService)
			acquired, err := fences.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{
				SessionRef: active.SessionRef, OwnerID: "host-a",
			})
			if err != nil {
				t.Fatal(err)
			}
			if !session.SessionFenceHasClaim(acquired) {
				t.Fatal("acquired fence has no bearer claim")
			}
			observed, err := fences.SessionFence(ctx, active.SessionRef)
			if err != nil {
				t.Fatal(err)
			}
			if session.SessionFenceHasClaim(observed) {
				t.Fatal("observed fence leaked its bearer claim")
			}
			forged, err := session.NewSessionFenceClaim(observed)
			if err != nil {
				t.Fatal(err)
			}
			if err := fences.ReleaseSessionFence(ctx, session.SessionFenceReleaseRequest(forged)); !errors.Is(err, session.ErrFenceConflict) {
				t.Fatalf("forged release error = %v, want ErrFenceConflict", err)
			}
			message := model.NewTextMessage(model.RoleUser, "forged mutation")
			guard := session.RuntimeMutationGuard(session.ContextWithRuntimeFence(ctx, forged))
			if _, err := service.AppendEvent(ctx, session.AppendEventRequest{
				SessionRef:    active.SessionRef,
				MutationGuard: guard,
				Event:         &session.Event{Type: session.EventTypeUser, Message: &message},
			}); !errors.Is(err, session.ErrFenceConflict) {
				t.Fatalf("forged mutation error = %v, want ErrFenceConflict", err)
			}
			durable, err := fences.SessionFence(ctx, active.SessionRef)
			if err != nil || durable.FenceID != acquired.FenceID || durable.FencingToken != acquired.FencingToken {
				t.Fatalf("durable fence after forgery = %#v, %v; want %#v", durable, err, acquired)
			}
			if err := fences.ReleaseSessionFence(ctx, session.SessionFenceReleaseRequest(acquired)); err != nil {
				t.Fatalf("release acquired fence: %v", err)
			}
		})
	}
}

func TestSessionFenceServiceConformance(t *testing.T) {
	t.Parallel()

	for _, store := range []string{"memory", "file"} {
		store := store
		t.Run(store, func(t *testing.T) {
			t.Parallel()
			clock := &fenceTestClock{now: time.Unix(100, 0)}
			var service session.Service
			var priorHostFences session.PriorHostFenceService
			var reopen func() (session.Service, session.PriorHostFenceService)
			switch store {
			case "memory":
				base, prior := inmemory.NewStoreWithPriorHostFences(inmemory.Config{Clock: clock.Now}, allowPriorHostFence)
				service = base
				priorHostFences = prior
				reopen = func() (session.Service, session.PriorHostFenceService) { return base, prior }
			case "file":
				root := t.TempDir()
				base, prior := sessionfile.NewStoreWithPriorHostFences(sessionfile.Config{RootDir: root, Clock: clock.Now}, allowPriorHostFence)
				service = base
				priorHostFences = prior
				reopen = func() (session.Service, session.PriorHostFenceService) {
					reopened, reopenedPrior := sessionfile.NewStoreWithPriorHostFences(sessionfile.Config{RootDir: root, Clock: clock.Now}, allowPriorHostFence)
					return reopened, reopenedPrior
				}
			}
			ctx := context.Background()
			active, err := service.StartSession(ctx, session.StartSessionRequest{
				AppName: "caelis", UserID: "user-1", PreferredSessionID: "fence-session",
			})
			if err != nil {
				t.Fatal(err)
			}
			fences := service.(session.SessionFenceService)
			first, err := fences.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{
				SessionRef: active.SessionRef, OwnerID: "host-a",
			})
			if err != nil {
				t.Fatalf("AcquireSessionFence() error = %v", err)
			}
			if first.FenceID == "" || first.OwnerID != "host-a" {
				t.Fatalf("first fence = %#v", first)
			}
			if first.FencingToken == 0 {
				t.Fatalf("first fencing token = %d, want positive", first.FencingToken)
			}
			if _, err := fences.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{
				SessionRef: active.SessionRef, OwnerID: "host-a",
			}); !errors.Is(err, session.ErrFenceConflict) {
				t.Fatalf("same-owner second acquisition error = %v, want ErrFenceConflict", err)
			}

			reopenedService, reopenedPriorHostFences := reopen()
			reopened := reopenedService.(session.SessionFenceService)
			second, err := reopenedPriorHostFences.ReplacePriorHostSessionFence(ctx, session.AcquireSessionFenceRequest{
				SessionRef: active.SessionRef, OwnerID: "host-b",
			})
			if err != nil || second.OwnerID != "host-b" || second.FencingToken <= first.FencingToken {
				t.Fatalf("new Host takeover = %#v, %v", second, err)
			}
			if err := fences.ReleaseSessionFence(ctx, session.SessionFenceReleaseRequest(first)); !errors.Is(err, session.ErrFenceConflict) {
				t.Fatalf("stale Host release error = %v, want ErrFenceConflict", err)
			}
			clock.Advance(24 * time.Hour)
			if _, err := reopened.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{
				SessionRef: active.SessionRef, OwnerID: "host-b",
			}); !errors.Is(err, session.ErrFenceConflict) {
				t.Fatalf("same Host acquire after clock advance = %v, want ErrFenceConflict", err)
			}
			takenOver, err := priorHostFences.ReplacePriorHostSessionFence(ctx, session.AcquireSessionFenceRequest{
				SessionRef: active.SessionRef, OwnerID: "host-c",
			})
			if err != nil || takenOver.OwnerID != "host-c" || takenOver.FenceID == second.FenceID || takenOver.FencingToken <= second.FencingToken {
				t.Fatalf("later Host takeover = %#v, %v", takenOver, err)
			}

			staleGuard := session.MutationGuard{Authority: session.MutationAuthorityRuntime, FenceID: second.FenceID, OwnerID: second.OwnerID, FencingToken: second.FencingToken}
			assertFenceGuardedMutations(t, service, active.SessionRef, staleGuard, takenOver)
		})
	}
}

func TestSessionFenceReleaseAndFenceConformance(t *testing.T) {
	t.Parallel()
	for _, store := range []string{"memory", "file"} {
		store := store
		t.Run(store, func(t *testing.T) {
			t.Parallel()
			clock := &fenceTestClock{now: time.Unix(1_000, 0)}
			var service session.Service
			var priorHostFences session.PriorHostFenceService
			switch store {
			case "memory":
				service, priorHostFences = inmemory.NewStoreWithPriorHostFences(inmemory.Config{Clock: clock.Now}, allowPriorHostFence)
			case "file":
				service, priorHostFences = sessionfile.NewStoreWithPriorHostFences(sessionfile.Config{RootDir: t.TempDir(), Clock: clock.Now}, allowPriorHostFence)
			}
			active, err := service.StartSession(context.Background(), session.StartSessionRequest{
				AppName: "caelis", UserID: "user-1", PreferredSessionID: "fence-boundary",
			})
			if err != nil {
				t.Fatal(err)
			}
			fences := service.(session.SessionFenceService)
			old, err := fences.AcquireSessionFence(context.Background(), session.AcquireSessionFenceRequest{
				SessionRef: active.SessionRef, OwnerID: "old-owner",
			})
			if err != nil {
				t.Fatal(err)
			}
			clock.Advance(365 * 24 * time.Hour)
			current, err := priorHostFences.ReplacePriorHostSessionFence(context.Background(), session.AcquireSessionFenceRequest{
				SessionRef: active.SessionRef, OwnerID: "new-owner",
			})
			if err != nil {
				t.Fatalf("new Host takeover = %v", err)
			}
			err = fences.ReleaseSessionFence(context.Background(), session.SessionFenceReleaseRequest(old))
			requireFenceConflictDetail(t, err, "fence identity, owner, or claim mismatch")

			message := model.NewTextMessage(model.RoleUser, "old owner")
			oldGuard := session.MutationGuard{
				Authority: session.MutationAuthorityRuntime, FenceID: old.FenceID,
				OwnerID: old.OwnerID, FencingToken: old.FencingToken,
			}
			_, err = service.AppendEvent(context.Background(), session.AppendEventRequest{
				SessionRef: active.SessionRef, MutationGuard: oldGuard,
				Event: &session.Event{Type: session.EventTypeUser, Message: &message},
			})
			requireFenceConflictDetail(t, err, "runtime fencing token is stale")

			if err := fences.ReleaseSessionFence(context.Background(), session.SessionFenceReleaseRequest(current)); err != nil {
				t.Fatal(err)
			}
			_, err = service.AppendEvent(context.Background(), session.AppendEventRequest{
				SessionRef: active.SessionRef, MutationGuard: oldGuard,
				Event: &session.Event{Type: session.EventTypeUser, Message: &message},
			})
			requireFenceConflictDetail(t, err, "runtime fence is absent")
		})
	}
}

func assertFenceGuardedMutations(
	t *testing.T,
	service session.Service,
	ref session.SessionRef,
	stale session.MutationGuard,
	current session.SessionFence,
) {
	t.Helper()
	user := model.NewTextMessage(model.RoleUser, "stale append")
	if _, err := service.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: ref, MutationGuard: stale, Event: &session.Event{Type: session.EventTypeUser, Message: &user},
	}); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("stale AppendEvent error = %v, want ErrFenceConflict", err)
	}
	batch := service.(session.EventBatchService)
	if _, err := batch.AppendEvents(context.Background(), session.AppendEventsRequest{
		SessionRef: ref, MutationGuard: stale, Events: []*session.Event{{Type: session.EventTypeUser, Message: &user}},
	}); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("stale AppendEvents error = %v, want ErrFenceConflict", err)
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
	}); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("stale compound mutation error = %v, want ErrFenceConflict", err)
	}
	if _, err := service.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: ref, Event: &session.Event{Type: session.EventTypeUser, Message: &user},
	}); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("unscoped AppendEvent error = %v, want ErrFenceConflict", err)
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
	}); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("unfenced handoff AppendEvent error = %v, want ErrFenceConflict", err)
	}
	for _, purpose := range []session.ControlMutationPurpose{
		session.ControlMutationPurposeLifecycle,
		session.ControlMutationPurposeConfiguration,
	} {
		if _, err := service.AppendEvent(context.Background(), session.AppendEventRequest{
			SessionRef: ref, MutationGuard: session.ControlMutationGuard(purpose), Event: &session.Event{Type: session.EventTypeUser, Message: &controlMessage},
		}); !errors.Is(err, session.ErrFenceConflict) {
			t.Fatalf("overlapping %s AppendEvent error = %v, want ErrFenceConflict", purpose, err)
		}
	}
	staleControl := session.MutationGuard{
		Authority: session.MutationAuthorityControl, Purpose: session.ControlMutationPurposeHandoff,
		FenceID: stale.FenceID, OwnerID: stale.OwnerID, FencingToken: stale.FencingToken,
	}
	if _, err := service.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: ref, MutationGuard: staleControl, Event: &session.Event{Type: session.EventTypeUser, Message: &controlMessage},
	}); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("stale fenced handoff AppendEvent error = %v, want ErrFenceConflict", err)
	}
	placedCtx := session.ContextWithRuntimeFence(context.Background(), current)
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
	}); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("stale UpdateState fence error = %v, want ErrFenceConflict", err)
	}
	if _, err := service.ReplaceState(context.Background(), session.ReplaceStateRequest{
		SessionRef: ref, ExpectedRevision: &currentSession.Revision, State: map[string]any{"unscoped": true},
	}); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("unscoped ReplaceState error = %v, want ErrFenceConflict", err)
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
		MutationGuard: session.ControlMutationGuardWithRuntimeFence(placedCtx, session.ControlMutationPurposeHandoff),
		Event:         &session.Event{Type: session.EventTypeUser, Message: &controlMessage},
	}); err != nil {
		t.Fatalf("matching fenced handoff AppendEvent error = %v", err)
	}
	if err := service.(session.SessionFenceService).ReleaseSessionFence(context.Background(), session.SessionFenceReleaseRequest(current)); err != nil {
		t.Fatalf("ReleaseSessionFence(current) error = %v", err)
	}
	if _, err := service.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef:    ref,
		MutationGuard: session.ControlMutationGuardWithRuntimeFence(placedCtx, session.ControlMutationPurposeHandoff),
		Event:         &session.Event{Type: session.EventTypeUser, Message: &controlMessage},
	}); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("late fenced handoff AppendEvent error = %v, want ErrFenceConflict", err)
	}
	if _, err := service.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef:    ref,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeHandoff),
		Event:         &session.Event{Type: session.EventTypeUser, Message: &controlMessage},
	}); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("quiescent unfenced handoff AppendEvent error = %v, want ErrFenceConflict", err)
	}
	if _, err := service.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef:    ref,
		MutationGuard: session.ControlMutationGuard("future_unknown"),
		Event:         &session.Event{Type: session.EventTypeUser, Message: &controlMessage},
	}); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("unknown control purpose AppendEvent error = %v, want ErrFenceConflict", err)
	}
	if _, err := service.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef:    ref,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeConfiguration),
		Event:         &session.Event{Type: session.EventTypeUser, Message: &controlMessage},
	}); err != nil {
		t.Fatalf("quiescent configuration AppendEvent error = %v", err)
	}

	fences := service.(session.SessionFenceService)
	fresh, err := fences.AcquireSessionFence(context.Background(), session.AcquireSessionFenceRequest{
		SessionRef: ref, OwnerID: current.OwnerID,
	})
	if err != nil {
		t.Fatalf("same-owner reacquire error = %v", err)
	}
	if fresh.FenceID == current.FenceID || fresh.FencingToken <= current.FencingToken {
		t.Fatalf("same-owner fresh fence = %#v, want distinct FenceID and increasing fence after %#v", fresh, current)
	}
	if err := fences.ReleaseSessionFence(context.Background(), session.SessionFenceReleaseRequest(fresh)); err != nil {
		t.Fatalf("release same-owner fresh fence error = %v", err)
	}
}

func requireFenceConflictDetail(t *testing.T, err error, detail string) {
	t.Helper()
	var conflict *session.FenceConflictError
	if !errors.As(err, &conflict) || !strings.Contains(conflict.Detail, detail) {
		t.Fatalf("fence conflict = %v, want detail containing %q", err, detail)
	}
}

type fenceTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fenceTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fenceTestClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}
