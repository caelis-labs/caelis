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
	return a.Reviewer.ReviewApproval(ctx, req)
}

var _ approval.Approver = Approver{}
