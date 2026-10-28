package controlplane

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

type lostIdleReleaseStore struct {
	*inmemory.Store
	mu       sync.Mutex
	requests []session.ReleaseForIdleCompactionRequest
	ordinary int
}

func (s *lostIdleReleaseStore) ReleaseSessionFenceForIdleCompaction(ctx context.Context, req session.ReleaseForIdleCompactionRequest) (session.IdleCompactionWindow, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	first := len(s.requests) == 1
	s.mu.Unlock()
	window, err := s.Store.ReleaseSessionFenceForIdleCompaction(ctx, req)
	if err == nil && first {
		return session.IdleCompactionWindow{}, errors.New("fixture: unknown release reply")
	}
	return window, err
}
func (s *lostIdleReleaseStore) ReleaseSessionFence(ctx context.Context, req session.ReleaseSessionFenceRequest) error {
	s.mu.Lock()
	s.ordinary++
	s.mu.Unlock()
	return s.Store.ReleaseSessionFence(ctx, req)
}
func TestIdleFenceHandoffReconcilesOriginalTransitionBeforeCompletion(t *testing.T) {
	store := &lostIdleReleaseStore{Store: inmemory.NewStore(inmemory.Config{})}
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	message := model.NewTextMessage(model.RoleAssistant, "completed canonical source")
	event, err := store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{Type: session.EventTypeAssistant, Message: &message}})
	if err != nil {
		t.Fatal(err)
	}
	runner := newFenceTestRunner("source-turn")
	delivered := make(chan session.IdleCompactionWindow, 2)
	wrapper, err := NewFencedRuntime(FencedRuntimeConfig{Runtime: fenceTestRuntime{runner: runner}, Fences: store, OwnerID: "host", PrepareIdle: func(ctx context.Context, ref session.SessionRef, fence session.SessionFence) (*IdleCompactionHandoff, error) {
		held, err := store.SessionFence(ctx, ref)
		if err != nil || held.FenceID != fence.FenceID {
			return nil, errors.New("handoff lost original fence")
		}
		loaded, err := store.LoadSession(ctx, session.LoadSessionRequest{SessionRef: ref})
		if err != nil {
			return nil, err
		}
		stamp, err := session.CheckpointSourceStamp(loaded)
		if err != nil {
			return nil, err
		}
		source := session.IdleCompactionSource{HostID: "host", ActivationID: "activation", TurnID: "source-turn", JobID: "job", Stamp: stamp, ThroughSeq: event.Seq, Coverage: session.IdleCompactionCoverage{Revision: 1, EventCount: 1, EndSeq: event.Seq, EndID: event.ID}}
		return &IdleCompactionHandoff{Release: session.NewIdleCompactionRelease(fence, source), Complete: func(window session.IdleCompactionWindow, err error) {
			if err != nil {
				t.Errorf("handoff completion: %v", err)
			}
			delivered <- window
		}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := wrapper.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	runner.finish()
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	window := <-delivered
	if window.SessionRef() != active.SessionRef {
		t.Fatal("completion preceded authoritative window readback")
	}
	store.mu.Lock()
	requests := append([]session.ReleaseForIdleCompactionRequest(nil), store.requests...)
	ordinary := store.ordinary
	store.mu.Unlock()
	if len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) || ordinary != 0 || len(delivered) != 0 {
		t.Fatalf("release was replaced or duplicated: requests=%d ordinary=%d", len(requests), ordinary)
	}
	if _, err = store.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "foreground"}); err != nil {
		t.Fatalf("idle opportunity blocked foreground: %v", err)
	}
}
