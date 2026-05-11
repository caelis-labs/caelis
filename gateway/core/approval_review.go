package core

import (
	"context"
	"fmt"
	"strings"

	sdkapproval "github.com/OnslaughtSnail/caelis/sdk/approval"
)

type ApprovalMode = sdkapproval.Mode

const (
	ApprovalModeAutoReview = sdkapproval.ModeAutoReview
	ApprovalModeManual     = sdkapproval.ModeManual
)

const (
	defaultAutoReviewMaxConsecutiveDenials = 3
	defaultAutoReviewMaxTotalDenials       = 5
)

func NormalizeApprovalMode(mode string) ApprovalMode {
	return sdkapproval.NormalizeMode(mode)
}

func CurrentApprovalMode(state map[string]any) ApprovalMode {
	if state == nil {
		return ApprovalModeAutoReview
	}
	if value, _ := state[StateCurrentSessionMode].(string); strings.TrimSpace(value) != "" {
		return NormalizeApprovalMode(value)
	}
	return ApprovalModeAutoReview
}

type ApprovalReviewRequest = sdkapproval.ReviewRequest
type ApprovalReviewResult = sdkapproval.ReviewResult
type ApprovalReviewer = sdkapproval.Reviewer

type denyingApprovalReviewer struct{}

func (denyingApprovalReviewer) ReviewApproval(_ context.Context, req ApprovalReviewRequest) (ApprovalReviewResult, error) {
	reason := "automatic approval reviewer is unavailable"
	if req.Approval != nil {
		if tool := strings.TrimSpace(req.Approval.ToolName); tool != "" {
			reason = fmt.Sprintf("automatic approval reviewer is unavailable for %s", tool)
		}
	}
	return ApprovalReviewResult{
		Approved:       false,
		Outcome:        string(ApprovalStatusRejected),
		Risk:           "unknown",
		Authorization:  "unknown",
		Rationale:      reason,
		DisplayText:    FormatApprovalReviewText(false, "unknown", "unknown", reason),
		DecisionSource: "auto-review",
	}, nil
}

var FormatApprovalReviewText = sdkapproval.FormatReviewText

func approvalReviewTerminalStatus(result ApprovalReviewResult) ApprovalReviewStatus {
	return sdkapproval.ReviewTerminalStatus(result)
}
