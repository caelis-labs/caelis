package approval

import "context"

type Request = ReviewRequest
type Decision = ReviewResult

// Approver returns a finalized approval decision for a request.
type Approver interface {
	Decide(context.Context, Request) (Decision, error)
}

// ReviewerAdapter adapts a raw reviewer into an approver by applying the shared
// approval finalization rules before returning a decision.
type ReviewerAdapter struct {
	Reviewer Reviewer
}

func (a ReviewerAdapter) Decide(ctx context.Context, req Request) (Decision, error) {
	if a.Reviewer == nil {
		return Decision{}, nil
	}
	result, err := a.Reviewer.ReviewApproval(ctx, req)
	if err != nil {
		return result, err
	}
	return FinalizeReviewResult(req.Approval, result), nil
}

type ApproverAdapter struct {
	Approver Approver
}

func (a ApproverAdapter) ReviewApproval(ctx context.Context, req Request) (Decision, error) {
	if a.Approver == nil {
		return Decision{}, nil
	}
	return a.Approver.Decide(ctx, req)
}
