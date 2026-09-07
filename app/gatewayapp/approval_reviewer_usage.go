package gatewayapp

import (
	"context"
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/kernel"
)

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
		if _, err := r.sessions.AppendEvent(cleanup, session.AppendEventRequest{SessionRef: req.SessionRef, MutationGuard: session.RuntimeMutationGuard(ctx), Event: receipt}); err != nil {
			return &guardianUsagePersistenceError{cause: err}
		}
	}
	return nil
}
