package agentreview

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/ports/approval"
)

func TestApproverFinalizesReviewResult(t *testing.T) {
	reviewer := staticReviewer{
		result: approval.ReviewResult{
			Approved:      true,
			Outcome:       string(approval.StatusApproved),
			OptionID:      "reject_once",
			Risk:          "low",
			Authorization: "high",
			Rationale:     "model selected reject",
		},
	}
	result, err := Approver{Reviewer: reviewer}.Decide(context.Background(), approval.Request{
		Approval: &approval.Payload{
			Options: []approval.Option{
				{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
				{ID: "reject_once", Name: "Reject once", Kind: "reject_once"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}

	if result.Approved || result.Outcome != string(approval.StatusSelected) || result.OptionID != "reject_once" {
		t.Fatalf("result = %#v, want finalized selected reject option", result)
	}
	if !strings.Contains(result.DisplayText, "denied") {
		t.Fatalf("DisplayText = %q, want finalized denial text", result.DisplayText)
	}
}

type staticReviewer struct {
	result approval.ReviewResult
}

func (r staticReviewer) ReviewApproval(context.Context, approval.ReviewRequest) (approval.ReviewResult, error) {
	return r.result, nil
}
