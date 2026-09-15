package gatewayapp

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/kernel"
)

type guardianInvocationCollector struct {
	mu       sync.Mutex
	attempts []model.Invocation
}

func (c *guardianInvocationCollector) collect(in model.Invocation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attempts = append(c.attempts, in)
}

// snapshot is consumed only after system-managed Run has drained its producer.
// The lock protects observer delivery; it does not replace that completion gate.
func (c *guardianInvocationCollector) snapshot() []model.Invocation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.attempts)
}

// guardianUsagePersistenceError is an accounting failure after actual model
// work. It must not turn a completed policy decision into a retryable decision.
type guardianUsagePersistenceError struct{ cause error }

func (e *guardianUsagePersistenceError) Error() string {
	return fmt.Sprintf("Guardian usage accounting could not be persisted: %v", e.cause)
}
func (e *guardianUsagePersistenceError) Unwrap() error { return e.cause }

func approvalAccountingKey(req kernel.ApprovalReviewRequest) string {
	return req.SessionRef.SessionID + "\x00" + req.ReviewID
}

func (r *guardianApprovalReviewer) persistGuardianInvocations(ctx context.Context, req kernel.ApprovalReviewRequest, attempts []model.Invocation) error {
	if len(attempts) == 0 {
		return nil
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	active, err := r.sessions.Session(cleanup, req.SessionRef)
	if err != nil {
		return &guardianUsagePersistenceError{cause: err}
	}
	for _, attempt := range attempts {
		receipt := session.NewModelInvocationReceipt(attempt, "guardian")
		receipt.SessionID = active.SessionID
		receipt.Scope = &session.EventScope{
			TurnID:     req.TurnID,
			Source:     "auto_review",
			Executor:   session.ActorRef{Kind: session.ActorKindSystem, ID: "guardian", Name: "guardian"},
			Controller: session.ControllerRef{Kind: active.Controller.Kind, ID: active.Controller.ControllerID, EpochID: active.Controller.EpochID},
		}
		receipt.Meta["usage_category"] = "auto_review"
		receipt.Meta["review_id"] = req.ReviewID
		// Empty or stale Runtime claims fail closed. A claim validated at review
		// admission permits receipt-only Control accounting after cancellation;
		// detached child accounting never borrows the parent's Runtime fence.
		guard := session.RuntimeMutationGuard(ctx)
		if guard.Authority != session.MutationAuthorityRuntime || (ctx.Err() != nil && ctx.Value(guardianReceiptAuthorityKey{}) == true) {
			guard = session.ControlMutationGuard(session.ControlMutationPurposeApproval)
		}
		if _, err := r.sessions.AppendEvent(cleanup, session.AppendEventRequest{SessionRef: req.SessionRef, MutationGuard: guard, Event: receipt}); err != nil {
			return &guardianUsagePersistenceError{cause: err}
		}
	}
	return nil
}
