package gatewayapp

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/internal/kernel"
)

type guardianDelayedModel struct {
	approvalReviewerFakeModel
	delay time.Duration
}

func (m *guardianDelayedModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			yield(nil, ctx.Err())
			return
		}
		for event, err := range m.approvalReviewerFakeModel.Generate(ctx, req) {
			if !yield(event, err) {
				return
			}
		}
	}
}

func TestGuardianSlowProviderAndQueueShareNinetySeconds(t *testing.T) {
	for _, delay := range []time.Duration{25 * time.Second, 60 * time.Second, 89 * time.Second, 100 * time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				service, active := newApprovalReviewerTestSession(t, t.Context())
				reviewer := newGuardianApprovalApprover(service)
				defer reviewer.Close()
				llm := &guardianDelayedModel{delay: delay}
				start := time.Now()
				result, err := reviewer.Decide(t.Context(), approvalReviewerTestRequest(active, llm, "inspect", nil))
				if delay < kernel.AutoReviewTimeout && (err != nil || !result.Approved) {
					t.Fatalf("slow valid review failed: %+v %v", result, err)
				}
				if delay > kernel.AutoReviewTimeout && (!errors.Is(err, context.DeadlineExceeded) || time.Since(start) != kernel.AutoReviewTimeout) {
					t.Fatalf("deadline settlement: %v after %v", err, time.Since(start))
				}
			})
		})
	}
	synctest.Test(t, func(t *testing.T) {
		service, active := newApprovalReviewerTestSession(t, t.Context())
		reviewer := newGuardianApprovalApprover(service)
		defer reviewer.Close()
		ctx, cancel := kernel.WithAutoReviewBudget(t.Context())
		defer cancel()
		time.Sleep(70 * time.Second) // elapsed Control admission/queue time
		llm := &guardianDelayedModel{delay: 25 * time.Second}
		start := time.Now()
		_, err := reviewer.Decide(ctx, approvalReviewerTestRequest(active, llm, "inspect", nil))
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 20*time.Second {
			t.Fatalf("queue reset clock: %v %v", err, time.Since(start))
		}
	})
}

func TestGuardianWindowKeepsLongUserMiddleWhenCapacityFits(t *testing.T) {
	text := strings.Repeat("User context. ", 2500) + "Never delete the original ledger." + strings.Repeat("More context. ", 2500)
	req := guardianWindowRequest(t, "long-user")
	source := guardianSource(1, session.EventTypeUser, text)
	history, items, err := guardianWindow(guardianConversationSnapshot{ParentEvents: []*session.Event{source}}, req, nil)
	if err != nil || items.MandatoryInputTooLarge || items.ContextTrimmed || !strings.Contains(guardianEventsText(history), text) {
		t.Fatalf("premature user trimming: %+v %v", items, err)
	}
}

func TestGuardianFileReceiptAdmissionRejectsForgedAndStaleClaims(t *testing.T) {
	store := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "caelis", UserID: "user", Workspace: session.WorkspaceRef{Key: "w", CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	fence, err := store.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	guard := session.RuntimeMutationGuard(session.ContextWithRuntimeFence(t.Context(), fence))
	if err := store.ValidateMutationGuard(t.Context(), active.SessionRef, guard); err != nil {
		t.Fatal(err)
	}
	forged := session.SessionFence{SessionRef: fence.SessionRef, FenceID: fence.FenceID, OwnerID: fence.OwnerID, FencingToken: fence.FencingToken}
	if err := store.ValidateMutationGuard(t.Context(), active.SessionRef, session.RuntimeMutationGuard(session.ContextWithRuntimeFence(t.Context(), forged))); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("forged claim: %v", err)
	}
	if err := store.ReleaseSessionFence(t.Context(), session.SessionFenceReleaseRequest(fence)); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateMutationGuard(t.Context(), active.SessionRef, guard); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("stale claim: %v", err)
	}
}
