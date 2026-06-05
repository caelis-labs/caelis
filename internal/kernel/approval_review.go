package kernel

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/approval"
)

type ApprovalMode = approval.Mode

const (
	ApprovalModeAutoReview = approval.ModeAutoReview
	ApprovalModeManual     = approval.ModeManual
)

func NormalizeApprovalMode(mode string) ApprovalMode {
	return approval.NormalizeMode(mode)
}

func CurrentApprovalMode(state map[string]any) ApprovalMode {
	return CurrentApprovalModeOrDefault(state, ApprovalModeAutoReview)
}

func CurrentApprovalModeOrDefault(state map[string]any, fallback ApprovalMode) ApprovalMode {
	if mode, ok := currentApprovalModeOverride(state); ok {
		return mode
	}
	return NormalizeApprovalMode(string(fallback))
}

type ApprovalReviewRequest = approval.Request
type ApprovalReviewResult = approval.Decision
type ApprovalReviewer = approval.Reviewer
type ApprovalApprover = approval.Approver

type denyingApprovalReviewer struct{}

func (denyingApprovalReviewer) ReviewApproval(ctx context.Context, req ApprovalReviewRequest) (ApprovalReviewResult, error) {
	return denyingApprovalApprover{}.Decide(ctx, req)
}

type denyingApprovalApprover struct{}

func (denyingApprovalApprover) Decide(_ context.Context, req ApprovalReviewRequest) (ApprovalReviewResult, error) {
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

var FormatApprovalReviewText = approval.FormatReviewText

func approvalReviewTerminalStatus(result ApprovalReviewResult) ApprovalReviewStatus {
	return approval.ReviewTerminalStatus(result)
}
