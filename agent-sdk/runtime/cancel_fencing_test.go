package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func TestACPControllerCancelPersistsFencedRequestWhileRemoteTurnIsLive(t *testing.T) {
	t.Parallel()

	service := inmemory.NewStore(inmemory.Config{})
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "acp-cancel-user", PreferredSessionID: "acp-fenced-cancel",
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err = service.BindController(context.Background(), session.BindControllerRequest{
		SessionRef:    active.SessionRef,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeLifecycle),
		Binding: session.ControllerBinding{
			Kind: session.ControllerKindACP, ControllerID: "external", EpochID: "epoch-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fence, err := service.AcquireSessionFence(context.Background(), session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "host-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	streaming := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseProducer := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseProducer)
	remote := &fencedCancelControllerHandle{streaming: streaming, release: release}
	core, err := New(testConfigWithACPForwarder(Config{
		Sessions: service, AgentFactory: chat.Factory{},
		Controllers: stubACPController{runTurn: func(context.Context, controller.TurnRequest) (controller.TurnHandle, error) {
			return remote, nil
		}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := session.ContextWithRuntimeFence(context.Background(), fence)
	run, err := core.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "wait"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseProducer()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := run.Handle.WaitCompletion(cleanupCtx); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("join released producer: %v", err)
		}
	})
	select {
	case <-streaming:
	case <-time.After(2 * time.Second):
		t.Fatal("remote controller stream did not start")
	}

	cancelled := run.Handle.Cancel()
	if cancelled.Err != nil {
		t.Fatalf("Cancel() error = %v, want fenced durable request", cancelled.Err)
	}
	state, err := core.RunState(t.Context(), active.SessionRef)
	if err != nil || state.ActiveRunID != run.Handle.RunID() {
		t.Fatalf("live producer lost: %+v %v", state, err)
	}

	releaseProducer()
	completionCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := run.Handle.WaitCompletion(completionCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitCompletion() error = %v, want cancellation", err)
	}
	state, err = core.RunState(t.Context(), active.SessionRef)
	if err != nil || state.ActiveRunID != "" {
		t.Fatalf("drained producer still live: %+v %v", state, err)
	}

}

type fencedCancelControllerHandle struct {
	streaming chan struct{}
	release   chan struct{}
	startOnce sync.Once
}

func (h *fencedCancelControllerHandle) WaitCompletion(ctx context.Context) error {
	h.startOnce.Do(func() { close(h.streaming) })
	// This fixture models a producer that remains live after cancellation.
	// Only the test's release barrier proves it has stopped; ctx.Done alone
	// must not let the terminal journal overtake the cancel_requested assertion.
	<-h.release
	return ctx.Err()
}

func (*fencedCancelControllerHandle) Cancel() controller.CancelResult {
	return controller.CancelResult{Status: controller.CancelStatusCancelled}
}

func (*fencedCancelControllerHandle) Close() error { return nil }
