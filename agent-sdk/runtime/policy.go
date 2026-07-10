package runtime

import (
	"context"
	"encoding/json"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type sandboxRuntimeProvider interface {
	SandboxRuntime() sandbox.Runtime
}

type approvalContext struct {
	ctx        context.Context
	requester  agent.ApprovalRequester
	runtime    *Runtime
	session    session.Session
	sessionRef session.SessionRef
	runID      string
	turnID     string
}

type policyWrappedTool struct {
	mode       string
	policy     policy.Mode
	session    session.Session
	sessionRef session.SessionRef
	state      map[string]any
	options    policy.ModeOptions
	tool       tool.Tool
	approval   approvalContext
}

type rejectedPolicyMode struct {
	name string
	err  error
}

func (m rejectedPolicyMode) Name() string { return strings.TrimSpace(m.name) }

func (m rejectedPolicyMode) DecideTool(context.Context, policy.ToolContext) (policy.Decision, error) {
	return policy.Decision{}, m.err
}

func (r *Runtime) wrapToolsForPolicy(
	activeSession session.Session,
	ref session.SessionRef,
	state map[string]any,
	spec agent.AgentSpec,
	approval approvalContext,
) []tool.Tool {
	if len(spec.Tools) == 0 {
		return spec.Tools
	}
	modeName, mode := r.policyForName(approval.ctx, r.policyMode(spec))
	options := modeOptionsFromSession(activeSession, spec)
	out := make([]tool.Tool, 0, len(spec.Tools))
	for _, one := range spec.Tools {
		if one == nil {
			continue
		}
		out = append(out, policyWrappedTool{
			mode:       modeName,
			policy:     mode,
			session:    session.CloneSession(activeSession),
			sessionRef: session.NormalizeSessionRef(ref),
			state:      session.CloneState(state),
			options:    policy.CloneModeOptions(options),
			tool:       one,
			approval:   approval,
		})
	}
	return out
}

func (r *Runtime) policyForName(ctx context.Context, modeName string) (string, policy.Mode) {
	normalized := normalizePolicyMode(modeName)
	if r == nil || r.policies == nil {
		return normalized, rejectedPolicyMode{name: normalized, err: &policy.ProfileError{
			Profile: normalized,
			Detail:  "policy registry is unavailable",
		}}
	}
	mode, ok, err := r.policies.Lookup(ctx, normalized)
	if err != nil {
		return normalized, rejectedPolicyMode{name: normalized, err: &policy.ProfileError{
			Profile: normalized,
			Detail:  "registry lookup failed",
			Err:     err,
		}}
	}
	if !ok || mode == nil {
		return normalized, rejectedPolicyMode{name: normalized, err: &policy.ProfileError{
			Profile: normalized,
			Detail:  "unknown policy profile",
		}}
	}
	return normalized, mode
}

func (t policyWrappedTool) Definition() tool.Definition {
	return tool.CloneDefinition(t.tool.Definition())
}

func (t policyWrappedTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if t.policy == nil {
		return tool.Result{}, &policy.ProfileError{Profile: t.mode, Detail: "policy mode is unavailable"}
	}
	input := policy.ToolContext{
		Session: t.session,
		State:   session.CloneState(t.state),
		Tool:    t.tool.Definition(),
		Call:    tool.CloneCall(call),
		Sandbox: t.describeSandbox(),
		Mode:    t.mode,
		Options: policy.CloneModeOptions(t.options),
	}
	decision, err := t.policy.DecideTool(ctx, input)
	if err != nil {
		return tool.Result{}, err
	}
	decision, err = policy.NormalizeDecision(t.mode, decision)
	if err != nil {
		return tool.Result{}, err
	}
	switch decision.Action {
	case policy.ActionAllow:
		call = tool.CloneCall(call)
		call.Metadata = mergeCallMetadata(call.Metadata, decision)
		return t.tool.Call(ctx, call)
	case policy.ActionAskApproval:
		return t.requestApproval(ctx, call, decision)
	case policy.ActionDeny:
		return policyDecisionResult(call, t.tool.Definition(), t.mode, decision), nil
	default:
		return tool.Result{}, &policy.DecisionError{Mode: t.mode, Detail: "unhandled normalized decision"}
	}
}

func (t policyWrappedTool) requestApproval(
	ctx context.Context,
	call tool.Call,
	decision policy.Decision,
) (tool.Result, error) {
	if decision.Approval == nil || t.approval.requester == nil {
		return policyDecisionResult(call, t.tool.Definition(), t.mode, decision), nil
	}
	if t.approval.runtime != nil {
		t.approval.runtime.setRunState(t.sessionRef.SessionID, agent.RunState{
			Status:          agent.RunLifecycleStatusWaitingApproval,
			ActiveRunID:     strings.TrimSpace(t.approval.runID),
			WaitingApproval: true,
			UpdatedAt:       t.approval.runtime.now(),
		})
		defer t.approval.runtime.setRunState(t.sessionRef.SessionID, agent.RunState{
			Status:      agent.RunLifecycleStatusRunning,
			ActiveRunID: strings.TrimSpace(t.approval.runID),
			UpdatedAt:   t.approval.runtime.now(),
		})
	}
	resp, err := t.approval.requester.RequestApproval(ctx, agent.ApprovalRequest{
		SessionRef: t.sessionRef,
		Session:    session.CloneSession(t.session),
		RunID:      strings.TrimSpace(t.approval.runID),
		TurnID:     strings.TrimSpace(t.approval.turnID),
		Tool:       t.tool.Definition(),
		Call:       tool.CloneCall(call),
		Approval:   cloneApproval(decision.Approval),
		Metadata:   mapsClone(decision.Metadata),
	})
	if err != nil {
		return tool.Result{}, err
	}
	if resp.Approved {
		call = tool.CloneCall(call)
		call.Metadata = mergeCallMetadata(call.Metadata, decision)
		return t.tool.Call(ctx, call)
	}
	return policyDecisionResultWithOutcome(call, t.tool.Definition(), t.mode, decision, resp), nil
}

func (t policyWrappedTool) describeSandbox() sandbox.Descriptor {
	if provider, ok := t.tool.(sandboxRuntimeProvider); ok && provider != nil {
		if runtime := provider.SandboxRuntime(); runtime != nil {
			return sandbox.CloneDescriptor(runtime.Describe())
		}
	}
	return sandbox.Descriptor{}
}

func mergeCallMetadata(meta map[string]any, decision policy.Decision) map[string]any {
	out := map[string]any{}
	for k, v := range meta {
		out[k] = v
	}
	if !constraintsIsZero(decision.Constraints) {
		out["sandbox_constraints"] = decision.Constraints
	}
	if decision.Metadata != nil {
		out["policy_metadata"] = decision.Metadata
	}
	return out
}

func policyDecisionResult(call tool.Call, def tool.Definition, mode string, decision policy.Decision) tool.Result {
	return policyDecisionResultWithOutcome(call, def, mode, decision, agent.ApprovalResponse{})
}

func policyDecisionResultWithOutcome(
	call tool.Call,
	def tool.Definition,
	mode string,
	decision policy.Decision,
	outcome agent.ApprovalResponse,
) tool.Result {
	approval := decision.Action == policy.ActionAskApproval
	errorText := strings.TrimSpace(firstNonEmpty(outcome.Reason, outcome.ReviewText, decision.Reason, "tool denied by policy"))
	payload := map[string]any{
		"error":         errorText,
		"error_code":    string(tool.ErrorCodePermissionDenied),
		"policy_mode":   strings.TrimSpace(mode),
		"policy_action": string(decision.Action),
		"tool_name":     strings.TrimSpace(def.Name),
	}
	if approval && decision.Approval != nil {
		payload["approval_required"] = true
		payload["approval"] = map[string]any{
			"tool_call": map[string]any{
				"id":     decision.Approval.ToolCall.ID,
				"name":   decision.Approval.ToolCall.Name,
				"kind":   decision.Approval.ToolCall.Kind,
				"title":  decision.Approval.ToolCall.Title,
				"status": decision.Approval.ToolCall.Status,
			},
		}
	}
	if approval {
		if strings.TrimSpace(outcome.Outcome) != "" {
			payload["approval_outcome"] = strings.TrimSpace(outcome.Outcome)
		}
		if strings.TrimSpace(outcome.OptionID) != "" {
			payload["approval_option_id"] = strings.TrimSpace(outcome.OptionID)
		}
		if strings.TrimSpace(outcome.Reason) != "" {
			payload["approval_reason"] = strings.TrimSpace(outcome.Reason)
		}
		if strings.TrimSpace(outcome.ReviewText) != "" {
			payload["approval_review_text"] = strings.TrimSpace(outcome.ReviewText)
		}
	}
	raw, _ := json.Marshal(payload)
	return tool.Result{
		ID:      strings.TrimSpace(call.ID),
		Name:    strings.TrimSpace(def.Name),
		IsError: true,
		Content: []model.Part{model.NewJSONPart(raw)},
	}
}

func cloneApproval(in *session.ProtocolApproval) *session.ProtocolApproval {
	if in == nil {
		return nil
	}
	out := session.CloneProtocolApproval(*in)
	return &out
}

func mapsClone(in map[string]any) map[string]any {
	return session.CloneState(in)
}

func firstNonEmpty(values ...string) string {
	for _, one := range values {
		if trimmed := strings.TrimSpace(one); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func constraintsIsZero(in sandbox.Constraints) bool {
	return in.Route == "" &&
		in.Backend == "" &&
		in.Permission == "" &&
		in.Isolation == "" &&
		in.Network == "" &&
		len(in.PathRules) == 0
}
