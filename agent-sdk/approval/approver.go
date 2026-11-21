package approval

import "context"

// Approver returns a finalized approval decision for a request.
type Approver interface {
	Decide(context.Context, ReviewRequest) (ReviewResult, error)
}

// ReviewerAdapter adapts a raw reviewer into an approver by applying the shared
// approval finalization rules before returning a decision.
type ReviewerAdapter struct {
	Reviewer Reviewer
}

func (a ReviewerAdapter) Decide(ctx context.Context, req ReviewRequest) (ReviewResult, error) {
	if a.Reviewer == nil {
		return ReviewResult{}, nil
	}
	result, err := a.Reviewer.ReviewApproval(ctx, req)
	if err != nil {
		return result, err
	}
	return FinalizeReviewResult(req.Approval, result), nil
}
