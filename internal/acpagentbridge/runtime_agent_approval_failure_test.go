package acpagentbridge

import (
	"context"
	"errors"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
)

type failedApprovalReviewer struct{ err error }

func (r failedApprovalReviewer) ReviewApproval(context.Context, approval.ReviewRequest) (approval.ReviewResult, error) {
	return approval.ReviewResult{}, r.err
}

func TestAutomaticApprovalFailureDoesNotBecomePolicyDenial(t *testing.T) {
	failure := errors.New("required evidence unavailable")
	requester := approvalRequester{reviewer: failedApprovalReviewer{failure}, mode: approval.ModeAutoReview}
	response, err := requester.RequestApproval(t.Context(), agent.ApprovalRequest{})
	if !errors.Is(err, failure) {
		t.Fatalf("error=%v, want original review failure", err)
	}
	if response.Approved || response.OptionID != "" {
		t.Fatalf("failure fabricated a decision: %+v", response)
	}
}
