package approval

import "context"

type Request = ReviewRequest
type Decision = ReviewResult

type Approver interface {
	Decide(context.Context, Request) (Decision, error)
}

type ReviewerAdapter struct {
	Reviewer Reviewer
}

func (a ReviewerAdapter) Decide(ctx context.Context, req Request) (Decision, error) {
	if a.Reviewer == nil {
		return Decision{}, nil
	}
	return a.Reviewer.ReviewApproval(ctx, req)
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
