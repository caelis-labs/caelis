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

const (
	defaultAutoReviewMaxConsecutiveDenials = 3
	defaultAutoReviewMaxTotalDenials       = 5
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

type turnManualApprover struct {
	turnCtx context.Context
	handle  *turnHandle
}

func (a turnManualApprover) Decide(ctx context.Context, req ApprovalReviewRequest) (ApprovalReviewResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if a.turnCtx == nil {
		a.turnCtx = context.Background()
	}
	if a.handle == nil {
		return ApprovalReviewResult{}, nil
	}
	runtimeReq := req.RuntimeRequest
	wait := a.handle.publishApproval(&runtimeReq)
	select {
	case decision := <-wait:
		return ApprovalReviewResult{
			Approved:       decision.Approved,
			Outcome:        decision.Outcome,
			OptionID:       decision.OptionID,
			Rationale:      decision.Reason,
			DisplayText:    decision.ReviewText,
			DecisionSource: string(ApprovalModeManual),
		}, nil
	case <-ctx.Done():
		return ApprovalReviewResult{}, ctx.Err()
	case <-a.turnCtx.Done():
		return ApprovalReviewResult{}, a.turnCtx.Err()
	}
}

var _ ApprovalApprover = turnManualApprover{}

var FormatApprovalReviewText = approval.FormatReviewText

func approvalReviewTerminalStatus(result ApprovalReviewResult) ApprovalReviewStatus {
	return approval.ReviewTerminalStatus(result)
}
