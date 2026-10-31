package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestAsyncCompactionDynamicMissingParentValidateDisablesSpeculation(t *testing.T) {
	h := newAsyncHarness(t, "")
	run, err := h.rt.Run(h.ctx, agent.RunRequest{SessionRef: h.active.SessionRef, Input: "continue", AgentSpec: agent.AgentSpec{ResolveModelRequest: func(context.Context) (agent.ModelRequestSnapshot, error) {
		return agent.ModelRequestSnapshot{Revision: "optional-validator", Model: h.llm, Tools: []tool.Tool{h.tool}}, nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Handle.Close(); _ = run.Handle.WaitCompletion(context.Background()) })
	receiveSnapshotValue(t, h.llm.foreground)
	h.llm.responses <- asyncToolResponse("first")
	next := receiveSnapshotValue(t, h.llm.foreground)
	if next.scope == nil {
		t.Fatal("async scope absent")
	}
	next.scope.mu.Lock()
	started := next.scope.started
	next.scope.mu.Unlock()
	if started {
		t.Fatal("Runtime-only guards enabled speculation without embedding validation")
	}
	h.llm.responses <- asyncTextResponse("done", 500)
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.llm.background:
		t.Fatal("background model dispatched without parent Validate")
	default:
	}
}

func TestAsyncCompactionDynamicRemovedParentValidatePreventsAdoption(t *testing.T) {
	h := newAsyncHarness(t, "")
	var removed atomic.Bool
	run, err := h.rt.Run(h.ctx, agent.RunRequest{SessionRef: h.active.SessionRef, Input: "continue", AgentSpec: agent.AgentSpec{ResolveModelRequest: func(ctx context.Context) (agent.ModelRequestSnapshot, error) {
		snapshot, err := h.source.resolve(ctx)
		if removed.Load() {
			// Same revision/model/catalog; only embedding validation disappears.
			snapshot.Validate = nil
		}
		return snapshot, err
	}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Handle.Close(); _ = run.Handle.WaitCompletion(context.Background()) })
	next := h.startCandidate(t)
	h.completeCandidate(t, next.scope)
	removed.Store(true)
	h.llm.responses <- asyncToolResponse("second")
	receiveSnapshotValue(t, h.secondTool)
	close(h.releaseSecond)
	after := receiveSnapshotValue(t, h.llm.foreground)
	if strings.Contains(asyncMessagesText(after.request.Messages), "CONTEXT CHECKPOINT") {
		t.Fatal("candidate adopted after embedding validation disappeared")
	}
	h.llm.responses <- asyncTextResponse("done", 500)
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := compact.LatestCompactEvent(h.load(t).Events); ok {
		t.Fatal("candidate persisted without current embedding validation")
	}
}

// A custom Engine may support overflow without ForceEngine, so its provider is
// not wrapped by the request watermark gate and cannot supply captured authority.
type overflowOnlyCompactor struct{ compact.Engine }

func TestExperimentalDynamicRecoveryRejectsMissingRequestAuthority(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async_%t", async), func(t *testing.T) {
			h := newAsyncHarness(t, "")
			h.rt.compaction.Async.Enabled = async
			compactor := &snapshotOverflowCompactor{err: errors.New("unguarded overflow reached generator")}
			h.rt.compactor = &overflowOnlyCompactor{Engine: compactor}
			run := h.run(t)
			receiveSnapshotValue(t, h.llm.foreground)
			h.llm.responses <- snapshotResponse{err: &model.ContextOverflowError{Cause: errors.New("fixture overflow")}}
			err := run.Handle.WaitCompletion(t.Context())
			if err == nil || !strings.Contains(err.Error(), "dynamic compaction recovery requires request authority") {
				t.Fatalf("missing request authority was not rejected: %v", err)
			}
			if compactor.request.Model != nil || compactor.prepareCalls != 0 {
				t.Fatal("generation started without captured authority")
			}
			if _, _, ok := compact.LatestCompactEvent(h.load(t).Events); ok {
				t.Fatal("unguarded dynamic recovery committed a checkpoint")
			}
		})
	}
}
