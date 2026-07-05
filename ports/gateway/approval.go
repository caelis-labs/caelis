package gateway

import "github.com/caelis-labs/caelis/agent-sdk/approval"

type ApprovalMode = approval.Mode

const (
	ApprovalModeAutoReview = approval.ModeAutoReview
	ApprovalModeManual     = approval.ModeManual
)

func NormalizeApprovalMode(mode string) ApprovalMode {
	return approval.NormalizeMode(mode)
}

func CurrentApprovalMode(state map[string]any) ApprovalMode {
	return approval.CurrentMode(state)
}

func CurrentApprovalModeOrDefault(state map[string]any, fallback ApprovalMode) ApprovalMode {
	return approval.CurrentModeOrDefault(state, fallback)
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
