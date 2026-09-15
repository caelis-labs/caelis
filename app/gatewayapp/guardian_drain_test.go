package gatewayapp

import (
	"context"
	"errors"
	"iter"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type guardianCleanupBarrierModel struct{ started, cancelled, release chan struct{} }

func (*guardianCleanupBarrierModel) Name() string { return "guardian-drain" }
func (*guardianCleanupBarrierModel) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalls: true, Streaming: true}
}
func (m *guardianCleanupBarrierModel) Generate(ctx context.Context, _ *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		close(m.started)
		<-ctx.Done()
		close(m.cancelled)
		<-m.release
		yield(&model.StreamEvent{Response: &model.Response{Usage: model.Usage{TotalTokens: 12}}}, ctx.Err())
	}
}

func TestGuardianCancellationSettlesBeforeDrainAndPersistsLateReceipt(t *testing.T) {
	store, active := newApprovalReviewerTestSession(t, context.Background())
	fence, err := store.(session.SessionFenceService).AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(session.ContextWithRuntimeFence(t.Context(), fence))
	defer cancel()
	llm := &guardianCleanupBarrierModel{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	reviewer := newGuardianApprovalApprover(store)
	done := make(chan error, 1)
	go func() {
		_, err := reviewer.Decide(ctx, approvalReviewerTestRequest(active, llm, "inspect", nil))
		done <- err
	}()
	select {
	case <-llm.started:
	case err := <-done:
		t.Fatalf("Guardian never started: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("Guardian never started")
	}
	cancel()
	<-llm.cancelled
	var early bool
	select {
	case err = <-done:
		early = true
	case <-time.After(50 * time.Millisecond):
	}
	if err := store.(session.SessionFenceService).ReleaseSessionFence(t.Context(), session.SessionFenceReleaseRequest(fence)); err != nil {
		t.Fatal(err)
	}
	if resident := reviewer.residents[active.SessionID]; resident == nil || len(resident.lanes) != guardianResidentLaneCount-1 {
		t.Fatal("draining lane was released early")
	}
	close(llm.release)
	reviewer.reviews.Wait()
	defer reviewer.Close()
	if !early {
		select {
		case err = <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("producer failed to drain")
		}
	}
	if !early {
		t.Error("Decide did not settle while provider cleanup was blocked")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cause = %v", err)
	}
	events, readErr := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if readErr != nil {
		t.Fatal(readErr)
	}
	count, total := 0, 0
	for _, event := range events {
		if session.IsModelInvocationReceipt(event) {
			count++
			if usage := session.UsageSnapshotFromSessionEvent(event); usage != nil {
				total += usage.TotalTokens
			}
		}
	}
	if count != 1 || total != 12 {
		t.Errorf("parent receipts=%d tokens=%d, want 1/12 after original fence release", count, total)
	}
}
