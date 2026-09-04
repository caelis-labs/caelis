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
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest),
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
	remote := &fencedCancelControllerHandle{streaming: streaming, release: release}
	core, err := New(testConfigWithACPForwarder(Config{
		Sessions: service, AgentFactory: chat.Factory{},
		Controllers: stubACPController{runTurn: func(context.Context, controller.TurnRequest) (controller.TurnResult, error) {
			return controller.TurnResult{Handle: remote}, nil
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
	select {
	case <-streaming:
	case <-time.After(2 * time.Second):
		t.Fatal("remote controller stream did not start")
	}

	cancelled := run.Handle.Cancel()
	if cancelled.Err != nil {
		t.Fatalf("Cancel() error = %v, want fenced durable request", cancelled.Err)
	}
	events, err := service.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasExecutionStatus(events, run.Handle.RunID(), session.JournalKindRun, session.ExecutionCancelRequested) {
		t.Fatalf("events = %#v, want durable ACP cancel_requested", events)
	}

	close(release)
	if err := run.Handle.WaitCompletion(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitCompletion() error = %v, want cancellation", err)
	}
}

type fencedCancelControllerHandle struct {
	streaming chan struct{}
	release   chan struct{}
	startOnce sync.Once
}

func (h *fencedCancelControllerHandle) WaitCompletion(ctx context.Context) error {
	h.startOnce.Do(func() { close(h.streaming) })
	select {
	case <-h.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (*fencedCancelControllerHandle) Cancel() controller.CancelResult {
	return controller.CancelResult{Status: controller.CancelStatusCancelled}
}

func (*fencedCancelControllerHandle) Close() error { return nil }

func hasExecutionStatus(events []*session.Event, runID string, kind session.JournalKind, status session.ExecutionStatus) bool {
	for _, event := range events {
		if event == nil || event.Journal == nil || event.Journal.Execution == nil {
			continue
		}
		record := event.Journal.Execution
		if record.RunID == runID && record.Kind == kind && record.Status == status {
			return true
		}
	}
	return false
}
