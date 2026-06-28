// Package agentreview adapts model-backed approval reviewers to the approval port.
package agentreview

import (
	"context"

	"github.com/OnslaughtSnail/caelis/ports/approval"
)

type Approver struct {
	Reviewer approval.Reviewer
}

func (a Approver) Decide(ctx context.Context, req approval.Request) (approval.Decision, error) {
	if a.Reviewer == nil {
		return approval.Decision{}, nil
	}
	result, err := a.Reviewer.ReviewApproval(ctx, req)
	if err != nil {
		return result, err
	}
	return approval.FinalizeReviewResult(req.Approval, result), nil
}

var _ approval.Approver = Approver{}
