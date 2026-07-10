package runtime

import (
	"context"
	"iter"
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

	service := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "acp-cancel-user", PreferredSessionID: "acp-fenced-cancel",
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err = service.BindController(context.Background(), session.BindControllerRequest{
		SessionRef:    active.SessionRef,
		MutationGuard: session.ControlMutationGuard(),
		Binding: session.ControllerBinding{
			Kind: session.ControllerKindACP, ControllerID: "external", EpochID: "epoch-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := service.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
		SessionRef: active.SessionRef, OwnerID: "host-a", TTL: time.Minute,
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
	ctx := session.ContextWithRuntimeLease(context.Background(), lease)
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
	for range run.Handle.Events() {
	}
}

type fencedCancelControllerHandle struct {
	streaming chan struct{}
	release   chan struct{}
}

func (h *fencedCancelControllerHandle) Events() iter.Seq2[*session.Event, error] {
	return func(func(*session.Event, error) bool) {
		close(h.streaming)
		<-h.release
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
