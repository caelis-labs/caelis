package controlplane

import (
	"context"
	"errors"
	"iter"
	"sync"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

func TestParticipantPromptUsesSessionFenceWithoutOrchestrationWatchdog(t *testing.T) {
	t.Parallel()

	service := inmemory.NewStore(inmemory.Config{})
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "participant-envelope", PreferredSessionID: "participant-envelope",
	})
	if err != nil {
		t.Fatal(err)
	}
	mainRunner := newFenceTestRunner("main-run")
	inner := &participantEnvelopeRuntime{sessions: service, mainRunner: mainRunner, streams: &participantStreamService{}}
	ownerA := newParticipantEnvelopeRuntime(t, inner, service, "host-a")
	ownerB := newParticipantEnvelopeRuntime(t, inner, service, "host-b")

	mainRun, err := ownerA.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if ownerA.Streams() != inner.streams {
		t.Fatal("decorated Streams() did not preserve the underlying stream service")
	}
	attached, err := ownerA.AttachLiveRun(context.Background(), agent.AttachLiveRunRequest{SessionRef: active.SessionRef, RunID: mainRunner.RunID()})
	if err != nil {
		t.Fatal(err)
	}
	if attached.Handle != mainRun.Handle {
		t.Fatalf("attached handle = %T, want the existing outer decorated runner", attached.Handle)
	}
	if err := ownerA.ResolveApproval(context.Background(), agent.ResolveApprovalRequest{SessionRef: active.SessionRef}); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerA.AttachParticipant(context.Background(), agent.AttachParticipantRequest{SessionRef: active.SessionRef}); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerA.DetachParticipant(context.Background(), agent.DetachParticipantRequest{SessionRef: active.SessionRef}); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerB.PromptParticipant(context.Background(), agent.PromptParticipantRequest{
		SessionRef: active.SessionRef, ParticipantID: "reviewer", Input: "conflicting prompt",
	}); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("cross-owner participant prompt error = %v, want ErrFenceConflict", err)
	}
	mainRunner.finish()
	if err := drainControlplaneRunner(mainRun.Handle); err != nil {
		t.Fatal(err)
	}

	participantRun, err := ownerB.PromptParticipant(context.Background(), agent.PromptParticipantRequest{
		SessionRef: active.SessionRef, ParticipantID: "reviewer", Input: "review safely",
	})
	if err != nil {
		t.Fatalf("PromptParticipant() error = %v", err)
	}
	if err := drainControlplaneRunner(participantRun.Handle); err != nil {
		t.Fatalf("participant runner error = %v", err)
	}
	inner.mu.Lock()
	guard := inner.promptGuard
	promptCalls := inner.promptCalls
	participantRunner := inner.participantRunner
	inner.mu.Unlock()
	if promptCalls != 1 || guard.Authority != session.MutationAuthorityRuntime || guard.FencingToken == 0 {
		t.Fatalf("participant prompt calls=%d guard=%#v, want one fenced execution", promptCalls, guard)
	}
	if participantRunner == nil {
		t.Fatal("participant runner is nil")
		return
	}
	participantRunner.mu.Lock()
	cancelCalls := participantRunner.cancel
	participantRunner.mu.Unlock()
	if cancelCalls != 0 {
		t.Fatalf("participant runner Cancel calls = %d, want no orchestration watchdog", cancelCalls)
	}
}

func newParticipantEnvelopeRuntime(t *testing.T, inner agent.Runtime, sessions session.Service, owner string) *FencedRuntime {
	t.Helper()
	fences, ok := sessions.(session.SessionFenceService)
	if !ok {
		t.Fatal("session service does not support fences")
	}
	fenced, err := NewFencedRuntime(FencedRuntimeConfig{
		Runtime: inner, Fences: fences, OwnerID: owner,
	})
	if err != nil {
		t.Fatal(err)
	}
	return fenced
}

type participantEnvelopeRuntime struct {
	sessions          session.Service
	mainRunner        *fenceTestRunner
	mu                sync.Mutex
	promptCalls       int
	promptGuard       session.MutationGuard
	participantRunner *fenceTestRunner
	streams           stream.Service
	approvals         int
}

func (*participantEnvelopeRuntime) RunnerCompletionWaiterGuaranteed() {}

type participantStreamService struct{}

func (*participantStreamService) Read(context.Context, stream.ReadRequest) (stream.Snapshot, error) {
	return stream.Snapshot{}, nil
}

func (*participantStreamService) Subscribe(context.Context, stream.SubscribeRequest) iter.Seq2[*stream.Frame, error] {
	return func(func(*stream.Frame, error) bool) {}
}

func (r *participantEnvelopeRuntime) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	return agent.RunResult{Handle: r.mainRunner}, nil
}

func (*participantEnvelopeRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

func (r *participantEnvelopeRuntime) Streams() stream.Service { return r.streams }

func (r *participantEnvelopeRuntime) AttachLiveRun(context.Context, agent.AttachLiveRunRequest) (agent.RunResult, error) {
	return agent.RunResult{Handle: r.mainRunner}, nil
}

func (r *participantEnvelopeRuntime) ResolveApproval(context.Context, agent.ResolveApprovalRequest) error {
	r.mu.Lock()
	r.approvals++
	r.mu.Unlock()
	return nil
}

func (r *participantEnvelopeRuntime) AttachParticipant(_ context.Context, req agent.AttachParticipantRequest) (session.Session, error) {
	return r.sessions.Session(context.Background(), req.SessionRef)
}

func (r *participantEnvelopeRuntime) PromptParticipant(ctx context.Context, req agent.PromptParticipantRequest) (agent.RunResult, error) {
	guard := session.RuntimeMutationGuard(ctx)
	message := model.NewTextMessage(model.RoleUser, req.Input)
	if _, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: req.SessionRef, MutationGuard: guard,
		Event: &session.Event{IdempotencyKey: "participant-envelope:" + req.Input, Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Message: &message},
	}); err != nil {
		return agent.RunResult{}, err
	}
	runner := newFenceTestRunner("participant-run")
	runner.finish()
	r.mu.Lock()
	r.promptCalls++
	r.promptGuard = guard
	r.participantRunner = runner
	r.mu.Unlock()
	return agent.RunResult{Session: session.Session{SessionRef: req.SessionRef}, Handle: runner}, nil
}

func (r *participantEnvelopeRuntime) DetachParticipant(_ context.Context, req agent.DetachParticipantRequest) (session.Session, error) {
	return r.sessions.Session(context.Background(), req.SessionRef)
}
