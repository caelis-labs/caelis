package core

import (
	"context"
	"fmt"
	"strings"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

type ApprovalMode string

const (
	ApprovalModeAutoReview ApprovalMode = "auto-review"
	ApprovalModeManual     ApprovalMode = "manual"
)

const (
	defaultAutoReviewMaxConsecutiveDenials = 3
	defaultAutoReviewMaxTotalDenials       = 5
)

func NormalizeApprovalMode(mode string) ApprovalMode {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "manual":
		return ApprovalModeManual
	case "auto-review", "auto_review", "autoreview":
		return ApprovalModeAutoReview
	default:
		return ApprovalModeAutoReview
	}
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

type ApprovalReviewRequest struct {
	SessionRef     sdksession.SessionRef
	RunID          string
	TurnID         string
	Mode           ApprovalMode
	ReviewID       string
	Model          sdkmodel.LLM
	Approval       *ApprovalPayload
	RuntimeRequest sdkruntime.ApprovalRequest
}

type ApprovalReviewResult struct {
	Approved       bool
	Outcome        string
	OptionID       string
	Risk           string
	Authorization  string
	Rationale      string
	DisplayText    string
	DecisionSource string
}

type ApprovalReviewer interface {
	ReviewApproval(context.Context, ApprovalReviewRequest) (ApprovalReviewResult, error)
}

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

func FormatApprovalReviewText(approved bool, risk string, authorization string, rationale string) string {
	verdict := "denied"
	if approved {
		verdict = "approved"
	}
	risk = firstNonEmpty(strings.TrimSpace(risk), "unknown")
	authorization = firstNonEmpty(strings.TrimSpace(authorization), "unknown")
	rationale = firstNonEmpty(strings.TrimSpace(rationale), "no rationale provided")
	return fmt.Sprintf("Automatic approval review %s (risk: %s, authorization: %s): %s", verdict, risk, authorization, rationale)
}

func approvalReviewTerminalStatus(result ApprovalReviewResult) ApprovalReviewStatus {
	if result.Approved {
		return ApprovalReviewStatusApproved
	}
	return ApprovalReviewStatusDenied
}
