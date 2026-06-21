package gateway

import "github.com/OnslaughtSnail/caelis/ports/approval"

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

type ApprovalOption = approval.Option
type ApprovalStatus = approval.Status

const (
	ApprovalStatusPending  = approval.StatusPending
	ApprovalStatusApproved = approval.StatusApproved
	ApprovalStatusRejected = approval.StatusRejected
	ApprovalStatusSelected = approval.StatusSelected
)

type ApprovalReviewStatus = approval.ReviewStatus

const (
	ApprovalReviewStatusInProgress = approval.ReviewStatusInProgress
	ApprovalReviewStatusApproved   = approval.ReviewStatusApproved
	ApprovalReviewStatusDenied     = approval.ReviewStatusDenied
	ApprovalReviewStatusTimedOut   = approval.ReviewStatusTimedOut
	ApprovalReviewStatusFailed     = approval.ReviewStatusFailed
)

type ApprovalPayload = approval.Payload
type ApprovalReviewTrace = approval.ReviewTrace
type ApprovalReviewRequest = approval.Request
type ApprovalReviewResult = approval.Decision
type ApprovalReviewer = approval.Reviewer
type ApprovalApprover = approval.Approver

var FormatApprovalReviewText = approval.FormatReviewText
