package runtime

import (
	"context"
	"errors"
	"fmt"
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
	var releaseOnce sync.Once
	releaseProducer := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseProducer)
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
	events, err := service.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []session.JournalKind{session.JournalKindRun, session.JournalKindTurn} {
		if !hasExecutionStatus(events, run.Handle.RunID(), kind, session.ExecutionCancelRequested) ||
			hasExecutionStatus(events, run.Handle.RunID(), kind, session.ExecutionCancelled) {
			t.Fatalf("%s before producer release: %v, want durable cancel_requested without cancelled", kind, cancelFencingEventDiagnostics(events))
		}
	}

	releaseProducer()
	completionCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := run.Handle.WaitCompletion(completionCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitCompletion() error = %v, want cancellation", err)
	}
	events, err = service.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []session.JournalKind{session.JournalKindRun, session.JournalKindTurn} {
		if !hasExecutionStatus(events, run.Handle.RunID(), kind, session.ExecutionCancelled) {
			t.Fatalf("%s after producer release: %v, want durable cancelled", kind, cancelFencingEventDiagnostics(events))
		}
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

func cancelFencingEventDiagnostics(events []*session.Event) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		if event == nil {
			out = append(out, "nil event")
			continue
		}
		detail := fmt.Sprintf("type=%s", session.EventTypeOf(event))
		if event.Journal != nil && event.Journal.Execution != nil {
			record := event.Journal.Execution
			detail += fmt.Sprintf(" run=%q kind=%s status=%s", record.RunID, record.Kind, record.Status)
		}
		out = append(out, detail)
	}
	return out
}
