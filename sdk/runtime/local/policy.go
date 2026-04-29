package local

import (
	"context"
	"encoding/json"
	"strings"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdkpolicy "github.com/OnslaughtSnail/caelis/sdk/policy"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
)

type sandboxRuntimeProvider interface {
	SandboxRuntime() sdksandbox.Runtime
}

type approvalContext struct {
	ctx        context.Context
	requester  sdkruntime.ApprovalRequester
	runtime    *Runtime
	session    sdksession.Session
	sessionRef sdksession.SessionRef
	runID      string
	turnID     string
}

type policyWrappedTool struct {
	mode       string
	policy     sdkpolicy.Mode
	session    sdksession.Session
	sessionRef sdksession.SessionRef
	state      map[string]any
	options    sdkpolicy.ModeOptions
	tool       sdktool.Tool
	approval   approvalContext
}

func (r *Runtime) wrapToolsForPolicy(
	session sdksession.Session,
	ref sdksession.SessionRef,
	state map[string]any,
	spec sdkruntime.AgentSpec,
	approval approvalContext,
) []sdktool.Tool {
	if len(spec.Tools) == 0 || r.policies == nil {
		return spec.Tools
	}
	modeName := r.policyMode(spec)
	mode, ok, err := r.policies.Lookup(approval.ctx, modeName)
	if err != nil || !ok || mode == nil {
		return spec.Tools
	}
	options := modeOptionsFromSession(session, spec)
	out := make([]sdktool.Tool, 0, len(spec.Tools))
	for _, one := range spec.Tools {
		if one == nil {
			continue
		}
		out = append(out, policyWrappedTool{
			mode:       modeName,
			policy:     mode,
			session:    sdksession.CloneSession(session),
			sessionRef: sdksession.NormalizeSessionRef(ref),
			state:      sdksession.CloneState(state),
			options:    sdkpolicy.CloneModeOptions(options),
			tool:       one,
			approval:   approval,
		})
	}
	return out
}

func (t policyWrappedTool) Definition() sdktool.Definition {
	return sdktool.CloneDefinition(t.tool.Definition())
}

func (t policyWrappedTool) Call(ctx context.Context, call sdktool.Call) (sdktool.Result, error) {
	input := sdkpolicy.ToolContext{
		Session: t.session,
		State:   sdksession.CloneState(t.state),
		Tool:    t.tool.Definition(),
		Call:    sdktool.CloneCall(call),
		Sandbox: t.describeSandbox(),
		Mode:    t.mode,
		Options: sdkpolicy.CloneModeOptions(t.options),
	}
	decision, err := t.policy.DecideTool(ctx, input)
	if err != nil {
		return sdktool.Result{}, err
	}
	switch decision.Action {
	case sdkpolicy.ActionAllow, "":
		call = sdktool.CloneCall(call)
		call.Metadata = mergeCallMetadata(call.Metadata, decision)
		return t.tool.Call(ctx, call)
	case sdkpolicy.ActionAskApproval:
		return t.requestApproval(ctx, call, decision)
	default:
		return policyDecisionResult(call, t.tool.Definition(), t.mode, decision), nil
	}
}

func (t policyWrappedTool) requestApproval(
	ctx context.Context,
	call sdktool.Call,
	decision sdkpolicy.Decision,
) (sdktool.Result, error) {
	if decision.Approval == nil || t.approval.requester == nil {
		return policyDecisionResult(call, t.tool.Definition(), t.mode, decision), nil
	}
	if t.approval.runtime != nil {
		t.approval.runtime.setRunState(t.sessionRef.SessionID, sdkruntime.RunState{
			Status:          sdkruntime.RunLifecycleStatusWaitingApproval,
			ActiveRunID:     strings.TrimSpace(t.approval.runID),
			WaitingApproval: true,
			UpdatedAt:       t.approval.runtime.now(),
		})
		defer t.approval.runtime.setRunState(t.sessionRef.SessionID, sdkruntime.RunState{
			Status:      sdkruntime.RunLifecycleStatusRunning,
			ActiveRunID: strings.TrimSpace(t.approval.runID),
			UpdatedAt:   t.approval.runtime.now(),
		})
	}
	resp, err := t.approval.requester.RequestApproval(ctx, sdkruntime.ApprovalRequest{
		SessionRef: t.sessionRef,
		Session:    sdksession.CloneSession(t.session),
		RunID:      strings.TrimSpace(t.approval.runID),
		TurnID:     strings.TrimSpace(t.approval.turnID),
		Mode:       t.mode,
		Tool:       t.tool.Definition(),
		Call:       sdktool.CloneCall(call),
		Approval:   cloneApproval(decision.Approval),
		Metadata:   mapsClone(decision.Metadata),
	})
	if err != nil {
		return sdktool.Result{}, err
	}
	if resp.Approved {
		call = sdktool.CloneCall(call)
		call.Metadata = mergeCallMetadata(call.Metadata, decision)
		return t.tool.Call(ctx, call)
	}
	return policyDecisionResultWithOutcome(call, t.tool.Definition(), t.mode, decision, resp), nil
}

func (t policyWrappedTool) describeSandbox() sdksandbox.Descriptor {
	if provider, ok := t.tool.(sandboxRuntimeProvider); ok && provider != nil {
		if runtime := provider.SandboxRuntime(); runtime != nil {
			return sdksandbox.CloneDescriptor(runtime.Describe())
		}
	}
	return sdksandbox.Descriptor{}
}

func mergeCallMetadata(meta map[string]any, decision sdkpolicy.Decision) map[string]any {
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

func policyDecisionResult(call sdktool.Call, def sdktool.Definition, mode string, decision sdkpolicy.Decision) sdktool.Result {
	return policyDecisionResultWithOutcome(call, def, mode, decision, sdkruntime.ApprovalResponse{})
}

func policyDecisionResultWithOutcome(
	call sdktool.Call,
	def sdktool.Definition,
	mode string,
	decision sdkpolicy.Decision,
	outcome sdkruntime.ApprovalResponse,
) sdktool.Result {
	approval := decision.Action == sdkpolicy.ActionAskApproval
	payload := map[string]any{
		"error":         strings.TrimSpace(firstNonEmpty(decision.Reason, "tool denied by policy")),
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
	raw, _ := json.Marshal(payload)
	meta := map[string]any{
		"error":         payload["error"],
		"policy_mode":   payload["policy_mode"],
		"policy_action": payload["policy_action"],
	}
	if approval {
		meta["approval_required"] = true
		if strings.TrimSpace(outcome.Outcome) != "" {
			meta["approval_outcome"] = strings.TrimSpace(outcome.Outcome)
		}
		if strings.TrimSpace(outcome.OptionID) != "" {
			meta["approval_option_id"] = strings.TrimSpace(outcome.OptionID)
		}
	}
	return sdktool.Result{
		ID:      strings.TrimSpace(call.ID),
		Name:    strings.TrimSpace(def.Name),
		IsError: true,
		Content: []sdkmodel.Part{sdkmodel.NewJSONPart(raw)},
		Meta:    meta,
	}
}

func cloneApproval(in *sdksession.ProtocolApproval) *sdksession.ProtocolApproval {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func mapsClone(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := map[string]any{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, one := range values {
		if trimmed := strings.TrimSpace(one); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func constraintsIsZero(in sdksandbox.Constraints) bool {
	return in.Route == "" &&
		in.Backend == "" &&
		in.Permission == "" &&
		in.Isolation == "" &&
		in.Network == "" &&
		len(in.PathRules) == 0
}
