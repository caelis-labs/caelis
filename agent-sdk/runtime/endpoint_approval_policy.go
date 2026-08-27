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
		options = approval.NormalizeProtocolOptions(payload.Options)
	}
	resolved := approval.ResolveReviewResult(&approval.Payload{Options: options}, approval.ReviewResult{
		Approved:  approved,
		Rationale: strings.TrimSpace(reason),
	})
	response := approval.RuntimeResponseFromFinalReview(resolved)
	if approved && strings.TrimSpace(response.OptionID) == "" {
		return agent.ApprovalResponse{}, fmt.Errorf("agent-sdk/runtime: endpoint permission has no allow option")
	}
	return response, nil
}
