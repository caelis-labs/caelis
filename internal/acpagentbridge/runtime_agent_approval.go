package acpagentbridge

import (
	"context"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/semantic"
)

type approvalRequester struct {
	callbacks     acp.PromptCallbacks
	reviewer      approval.Reviewer
	modelResolver ApprovalModelResolver
	mode          approval.Mode
}

func (r approvalRequester) RequestApproval(
	ctx context.Context,
	req agent.ApprovalRequest,
) (agent.ApprovalResponse, error) {
	if r.reviewer != nil && r.mode != approval.ModeManual {
		return r.reviewApproval(ctx, req)
	}
	return r.requestClientPermission(ctx, req)
}

func (r approvalRequester) reviewApproval(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
	payload := approval.PayloadFromRuntimeRequest(req)
	var reviewModel model.LLM
	if r.modelResolver != nil {
		reviewModel, _ = r.modelResolver.ResolveApprovalModel(ctx, req.SessionRef)
	}
	result, err := approval.ReviewerAdapter{Reviewer: r.reviewer}.Decide(ctx, approval.ReviewRequest{
		SessionRef:     req.SessionRef,
		RunID:          strings.TrimSpace(req.RunID),
		TurnID:         strings.TrimSpace(req.TurnID),
		Mode:           r.mode,
		ReviewID:       approval.ReviewID("acp-approval-review", payload),
		Model:          reviewModel,
		Approval:       approval.ClonePayload(payload),
		RuntimeRequest: req,
	})
	if err != nil {
		rationale := "automatic approval review failed: " + err.Error()
		result = approval.FinalizeReviewResult(payload, approval.ReviewResult{
			Approved:       false,
			Outcome:        string(approval.StatusRejected),
			Risk:           "unknown",
			Authorization:  "unknown",
			Rationale:      rationale,
			DisplayText:    approval.FormatReviewText(false, "unknown", "unknown", rationale),
			DecisionSource: string(approval.ModeAutoReview),
		})
	}
	return approval.RuntimeResponseFromFinalReview(result), nil
}

func (r approvalRequester) requestClientPermission(
	ctx context.Context,
	req agent.ApprovalRequest,
) (agent.ApprovalResponse, error) {
	if r.callbacks == nil || req.Approval == nil {
		return agent.ApprovalResponse{}, nil
	}
	request, err := semantic.EncodePermissionRequest(req.SessionRef, req.Approval, nil)
	if err != nil {
		return agent.ApprovalResponse{}, err
	}
	response, err := r.callbacks.RequestPermission(ctx, request)
	if err != nil {
		return agent.ApprovalResponse{}, err
	}
	return semantic.DecodePermissionResponse(response, req.Approval), nil
}
