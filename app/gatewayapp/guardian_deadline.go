package gatewayapp

import (
	"context"
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/kernel"
)

type guardianReceiptAuthorityKey struct{}

// Decide settles the caller independently of producer cleanup. The tracked
// worker retains its lane until the producer is quiescent, including after a
// timeout; neither a late decision nor that lane can escape into another review.
func (r *guardianApprovalReviewer) Decide(ctx context.Context, req kernel.ApprovalReviewRequest) (kernel.ApprovalReviewResult, error) {
	if r == nil {
		return kernel.ApprovalReviewResult{}, fmt.Errorf("guardian is unavailable")
	}
	ctx, cancel := kernel.WithAutoReviewBudget(ctx)
	defer cancel()
	req.Approval = approval.ClonePayload(req.Approval)
	req.RuntimeRequest = approval.CloneRuntimeRequest(req.RuntimeRequest)
	// Validate the unforgeable parent claim before permitting this worker to
	// finish receipt-only accounting after the caller releases its fence.
	if guard := session.RuntimeMutationGuard(ctx); guard.Authority == session.MutationAuthorityRuntime {
		if validator, ok := r.sessions.(session.MutationGuardValidator); ok {
			if validator.ValidateMutationGuard(ctx, req.SessionRef, guard) == nil {
				ctx = context.WithValue(ctx, guardianReceiptAuthorityKey{}, true)
			}
		}
	}
	r.resourcesMu.Lock()
	if r.closed {
		r.resourcesMu.Unlock()
		return kernel.ApprovalReviewResult{}, context.Canceled
	}
	r.reviews.Add(1)
	r.resourcesMu.Unlock()
	type outcome struct {
		result kernel.ApprovalReviewResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		defer r.reviews.Done()
		result, err := r.decide(ctx, req)
		done <- outcome{result, err}
		if ctx.Err() != nil && r.diagnostics != nil {
			r.diagnostics.Info("Guardian producer drained", "review_id", req.ReviewID, "elapsed_ms", time.Since(kernel.AutoReviewStarted(ctx)).Milliseconds())
		}
	}()
	select {
	case result := <-done:
		if err := ctx.Err(); err != nil {
			return kernel.ApprovalReviewResult{}, err
		}
		return result.result, result.err
	case <-ctx.Done():
		if r.diagnostics != nil {
			r.diagnostics.Info("Guardian review settled before cleanup", "review_id", req.ReviewID, "elapsed_ms", time.Since(kernel.AutoReviewStarted(ctx)).Milliseconds(), "cause", ctx.Err())
		}
		return kernel.ApprovalReviewResult{}, ctx.Err()
	}
}
