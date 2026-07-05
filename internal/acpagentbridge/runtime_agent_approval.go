package acpagentbridge

import (
	"context"
	"maps"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/projector"
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
	projector := projector.EventProjector{}
	event := &session.Event{
		SessionID: strings.TrimSpace(req.SessionRef.SessionID),
		Protocol: &session.EventProtocol{
			Method:     session.ProtocolMethodRequestPermission,
			Permission: cloneProtocolApproval(req.Approval),
		},
	}
	request, ok, err := projector.ProjectPermissionRequest(event)
	if err != nil {
		return agent.ApprovalResponse{}, err
	}
	if !ok || request == nil {
		return agent.ApprovalResponse{}, nil
	}
	response, err := r.callbacks.RequestPermission(ctx, *request)
	if err != nil {
		return agent.ApprovalResponse{}, err
	}
	outcome := strings.TrimSpace(response.Outcome.Outcome)
	optionID := strings.TrimSpace(response.Outcome.OptionID)
	approved := false
	if outcome == "selected" {
		for _, item := range request.Options {
			if item.OptionID == optionID && strings.HasPrefix(strings.ToLower(strings.TrimSpace(item.Kind)), "allow") {
				approved = true
				break
			}
		}
	}
	return agent.ApprovalResponse{
		Outcome:  outcome,
		OptionID: optionID,
		Approved: approved,
	}, nil
}

func cloneProtocolApproval(in *session.ProtocolApproval) *session.ProtocolApproval {
	if in == nil {
		return nil
	}
	out := *in
	out.ToolCall.RawInput = maps.Clone(in.ToolCall.RawInput)
	if len(in.Options) > 0 {
		out.Options = append([]session.ProtocolApprovalOption(nil), in.Options...)
	}
	return &out
}
