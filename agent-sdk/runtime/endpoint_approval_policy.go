package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
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
	if r == nil || r.sessions == nil {
		return agent.ApprovalResponse{}, true, fmt.Errorf("agent-sdk/runtime: endpoint policy service is unavailable")
	}
	state, err := r.sessions.SnapshotState(ctx, request.SessionRef)
	if err != nil {
		return agent.ApprovalResponse{}, true, err
	}
	modeName, mode := r.policyForName(ctx, modeName)
	call := tool.CloneCall(request.Call)
	call.Input, err = endpointPolicyCallInput(call.Input, request.Approval)
	if err != nil {
		return agent.ApprovalResponse{}, true, err
	}
	definition := tool.CloneDefinition(request.Tool)
	definition.Name = endpointPolicyToolName(definition.Name, request.Approval)
	if endpointPolicyCommandCapability(request.Approval) {
		if definition.ExecutionRequirements == nil {
			definition.ExecutionRequirements = &tool.ExecutionRequirements{}
		}
		definition.ExecutionRequirements.Sandbox.CommandExec = true
	}
	call.Name = firstNonEmpty(call.Name, definition.Name)
	decision, err := mode.DecideTool(ctx, policy.ToolContext{
		Session: session.CloneSession(request.Session),
		State:   session.CloneState(state),
		Tool:    definition,
		Call:    call,
		Sandbox: sandbox.Descriptor{
			Backend:   sandbox.BackendHost,
			Isolation: sandbox.IsolationHost,
			DefaultConstraints: sandbox.Constraints{
				Route:      sandbox.RouteHost,
				Backend:    sandbox.BackendHost,
				Permission: sandbox.PermissionFullAccess,
				Isolation:  sandbox.IsolationHost,
			},
		},
		SandboxPolicy: sandbox.PolicySnapshot{
			Route:      sandbox.RouteHost,
			Backend:    sandbox.BackendHost,
			Permission: sandbox.PermissionFullAccess,
			Isolation:  sandbox.IsolationHost,
			Network:    sandbox.NetworkInherit,
		},
		Mode:    modeName,
		Options: modeOptionsFromSession(request.Session, agent.AgentSpec{}),
	})
	if err != nil {
		return agent.ApprovalResponse{}, true, err
	}
	decision, err = policy.NormalizeDecision(modeName, decision)
	if err != nil {
		return agent.ApprovalResponse{}, true, err
	}
	switch decision.Action {
	case policy.ActionAllow:
		response, responseErr := endpointPolicyApprovalResponse(request.Approval, true, decision.Reason)
		return response, true, responseErr
	case policy.ActionDeny:
		response, responseErr := endpointPolicyApprovalResponse(request.Approval, false, decision.Reason)
		return response, true, responseErr
	case policy.ActionAskApproval:
		return agent.ApprovalResponse{}, true, &policy.DecisionError{
			Mode:   modeName,
			Detail: "process-owned full-access policy unexpectedly requested approval",
		}
	default:
		return agent.ApprovalResponse{}, true, &policy.DecisionError{Mode: modeName, Detail: "unhandled normalized endpoint decision"}
	}
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

func endpointPolicyToolName(name string, payload *session.ProtocolApproval) string {
	if name != "" || payload == nil {
		return name
	}
	return firstNonEmpty(payload.ToolCall.Name, payload.ToolCall.Kind)
}

func endpointPolicyCommandCapability(payload *session.ProtocolApproval) bool {
	if payload == nil || !strings.EqualFold(strings.TrimSpace(payload.ToolCall.Kind), "execute") {
		return false
	}
	for _, key := range []string{"command", "cmd"} {
		if command, ok := payload.ToolCall.RawInput[key].(string); ok && strings.TrimSpace(command) != "" {
			return true
		}
	}
	return false
}

func endpointPolicyCallInput(raw json.RawMessage, payload *session.ProtocolApproval) (json.RawMessage, error) {
	if payload == nil || len(payload.ToolCall.RawInput) == 0 {
		return append(json.RawMessage(nil), raw...), nil
	}
	input := session.CloneState(payload.ToolCall.RawInput)
	if _, exists := input["command"]; !exists {
		if command, ok := input["cmd"].(string); ok && strings.TrimSpace(command) != "" {
			input["command"] = strings.TrimSpace(command)
		}
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("agent-sdk/runtime: encode endpoint policy input: %w", err)
	}
	return encoded, nil
}
