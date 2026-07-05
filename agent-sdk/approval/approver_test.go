package approval

import (
	"context"
	"strings"
	"testing"
)

func TestReviewerAdapterFinalizesReviewResult(t *testing.T) {
	reviewer := staticReviewer{
		result: ReviewResult{
			Approved:      true,
			Outcome:       string(StatusApproved),
			OptionID:      "reject_once",
			Risk:          "low",
			Authorization: "high",
			Rationale:     "model selected reject",
		},
	}
	result, err := ReviewerAdapter{Reviewer: reviewer}.Decide(context.Background(), Request{
		Approval: &Payload{
			Options: []Option{
				{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
				{ID: "reject_once", Name: "Reject once", Kind: "reject_once"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}

	if result.Approved || result.Outcome != string(StatusSelected) || result.OptionID != "reject_once" {
		t.Fatalf("result = %#v, want finalized selected reject option", result)
	}
	if !strings.Contains(result.DisplayText, "denied") {
		t.Fatalf("DisplayText = %q, want finalized denial text", result.DisplayText)
	}
}

type staticReviewer struct {
	result ReviewResult
}

func (r staticReviewer) ReviewApproval(context.Context, ReviewRequest) (ReviewResult, error) {
	return r.result, nil
}
