package runtime

import (
	"context"
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// resolveEndpointApprovalByPolicy handles process-owned policy decisions for
// ACP controllers, participants, and spawned subagents before they can enter
// the Host approval requester. Ordinary policy modes retain their existing ACP
// permission routing.
func (r *Runtime) resolveEndpointApprovalByPolicy(
	ctx context.Context,
	modeName string,
	request agent.ApprovalRequest,
) (agent.ApprovalResponse, bool, error) {
	modeName = normalizePolicyMode(modeName)
	if modeName != presets.ModeDangerFullAccess {
		return agent.ApprovalResponse{}, false, nil
	}
	// The profile name can originate from Agent metadata, but only the
	// process-owned registry may authorize the exceptional YOLO behavior.
	// Lookup failure owns the request and fails closed instead of falling back
	// to the Host approval path.
	if _, _, err := r.lookupPolicyMode(ctx, modeName); err != nil {
		return agent.ApprovalResponse{}, true, err
	}
	response, err := endpointPolicyApprovalResponse(request.Approval, true, "")
	return response, true, err
}

func endpointPolicyApprovalResponse(
	payload *session.ProtocolApproval,
	approved bool,
	reason string,
) (agent.ApprovalResponse, error) {
	options := make([]approval.Option, 0)
	if payload != nil {
		options = make([]approval.Option, 0, len(payload.Options))
		for _, option := range payload.Options {
			options = append(options, approval.Option{ID: option.ID, Name: option.Name, Kind: option.Kind})
		}
	}
	if err := approval.ValidateACPOptions(options); err != nil {
		return agent.ApprovalResponse{}, fmt.Errorf("agent-sdk/runtime: invalid endpoint permission options: %w", err)
	}
	decision := approval.OptionDecisionDeny
	if approved {
		decision = approval.OptionDecisionAllow
	}
	optionID, ok, err := approval.StrictOptionIDForDecision(options, decision)
	if err != nil {
		return agent.ApprovalResponse{}, fmt.Errorf("agent-sdk/runtime: resolve endpoint permission option: %w", err)
	}
	if !ok {
		return agent.ApprovalResponse{}, fmt.Errorf("agent-sdk/runtime: endpoint permission has no %s option", decision)
	}
	resolved := approval.ReviewResult{
		Outcome:   string(approval.StatusSelected),
		OptionID:  optionID,
		Approved:  approved,
		Rationale: strings.TrimSpace(reason),
	}
	response := approval.RuntimeResponseFromFinalReview(resolved)
	return response, nil
}
