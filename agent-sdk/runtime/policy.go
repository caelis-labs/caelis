package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
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
	mode          string
	policy        policy.Mode
	session       session.Session
	sessionRef    session.SessionRef
	state         map[string]any
	options       policy.ModeOptions
	sandboxPolicy sandbox.PolicySnapshot
	tool          tool.Tool
	approval      approvalContext
	mcpGrants     *MCPGrantStore
	mcpGrantOwner string
}

type rejectedPolicyMode struct {
	name string
	err  error
}

func (t policyWrappedTool) RuntimeTaskResultSource(toolbinding.Token) bool {
	return toolbinding.IsTaskResultSource(t.tool)
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
			mode:          modeName,
			policy:        mode,
			session:       session.CloneSession(activeSession),
			sessionRef:    session.NormalizeSessionRef(ref),
			state:         session.CloneState(state),
			options:       policy.CloneModeOptions(options),
			sandboxPolicy: sandbox.ClonePolicySnapshot(r.sandboxPolicy),
			tool:          one,
			approval:      approval,
			mcpGrants:     r.mcpGrants,
			mcpGrantOwner: r.mcpGrantOwner,
		})
	}
	return out
}

func (r *Runtime) policyForName(ctx context.Context, modeName string) (string, policy.Mode) {
	normalized, mode, err := r.lookupPolicyMode(ctx, modeName)
	if err != nil {
		return normalized, rejectedPolicyMode{name: normalized, err: err}
	}
	return normalized, mode
}

func (r *Runtime) lookupPolicyMode(ctx context.Context, modeName string) (string, policy.Mode, error) {
	normalized := normalizePolicyMode(modeName)
	if r == nil || r.policies == nil {
		return normalized, nil, &policy.ProfileError{
			Profile: normalized,
			Detail:  "policy registry is unavailable",
		}
	}
	mode, ok, err := r.policies.Lookup(ctx, normalized)
	if err != nil {
		return normalized, nil, &policy.ProfileError{
			Profile: normalized,
			Detail:  "registry lookup failed",
			Err:     err,
		}
	}
	if !ok || mode == nil {
		return normalized, nil, &policy.ProfileError{
			Profile: normalized,
			Detail:  "unknown policy profile",
		}
	}
	return normalized, mode, nil
}

func (t policyWrappedTool) Definition() tool.Definition {
	return tool.CloneDefinition(t.tool.Definition())
}

func (t policyWrappedTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	started := taskInvocationStart{time: time.Now()}
	if scope := taskScopeFromContext(t.approval.ctx); scope != nil {
		started.generation = scope.currentGeneration()
	}
	ctx = context.WithValue(ctx, taskInvocationStartKey{}, started)
	defer call.ModelStep.MarkAdmissionComplete()
	if t.policy == nil {
		return tool.Result{}, &policy.ProfileError{Profile: t.mode, Detail: "policy mode is unavailable"}
	}
	input := policy.ToolContext{
		Session:       t.session,
		State:         session.CloneState(t.state),
		Tool:          t.tool.Definition(),
		Call:          tool.CloneCall(call),
		Sandbox:       t.describeSandbox(),
		SandboxPolicy: sandbox.ClonePolicySnapshot(t.sandboxPolicy),
		Mode:          t.mode,
		Options:       policy.CloneModeOptions(t.options),
	}
	decision, err := t.policy.DecideTool(ctx, input)
	if err != nil {
		return tool.Result{}, err
	}
	decision, err = policy.NormalizeDecision(t.mode, decision)
	if err != nil {
		return tool.Result{}, err
	}
	// The Host's MCP source grant is independent of this call's policy. In
	// particular, a cached source grant cannot satisfy a custom per-call Ask.
	if tool.IsMCPDefinition(input.Tool) && decision.Action != policy.ActionDeny {
		grant, err := mcpGrantFor(input.Tool, t.session)
		if err != nil {
			return tool.Result{}, err
		}
		grant.Owner = t.mcpGrantOwner
		allowed, err := t.mcpGrants.Allows(grant, t.sessionRef.SessionID)
		if err != nil {
			return tool.Result{}, err
		}
		if !allowed {
			gate, err := presets.MCPApprovalDecision(input, decision.Constraints)
			if err != nil {
				return tool.Result{}, err
			}
			return t.requestApprovalWithContinuation(ctx, call, gate, &grant, func(ctx context.Context, _ tool.Call) (tool.Result, error) {
				return t.applyPolicyDecision(ctx, call, decision)
			})
		}
	}
	return t.applyPolicyDecision(ctx, call, decision)
}

func (t policyWrappedTool) applyPolicyDecision(ctx context.Context, call tool.Call, decision policy.Decision) (tool.Result, error) {
	switch decision.Action {
	case policy.ActionAllow:
		call.ModelStep.MarkAdmissionComplete()
		call = tool.CloneCall(call)
		call.Metadata = mergeCallMetadata(call.Metadata, decision)
		return t.tool.Call(ctx, call)
	case policy.ActionAskApproval:
		return t.requestApproval(ctx, call, decision)
	case policy.ActionDeny:
		call.ModelStep.MarkAdmissionComplete()
		return policyDecisionResult(call, t.tool.Definition(), decision), nil
	default:
		return tool.Result{}, &policy.DecisionError{Mode: t.mode, Detail: "unhandled normalized decision"}
	}
}

func (t policyWrappedTool) requestApproval(
	ctx context.Context,
	call tool.Call,
	decision policy.Decision,
) (tool.Result, error) {
	return t.requestApprovalWithContinuation(ctx, call, decision, nil, nil)
}

func (t policyWrappedTool) requestApprovalWithContinuation(
	ctx context.Context,
	call tool.Call,
	decision policy.Decision,
	gate *MCPGrant,
	afterApproved approvedToolCall,
) (tool.Result, error) {
	if decision.Approval == nil || (t.approval.requester == nil && t.approval.runtime == nil) {
		return policyDecisionResult(call, t.tool.Definition(), decision), nil
	}
	if afterApproved == nil {
		afterApproved = t.tool.Call
	}
	request := agent.ApprovalRequest{
		Origin:     &agent.ApprovalOrigin{WorkingDirectory: t.session.CWD, Role: agent.ApprovalRoleMain, Endpoint: agent.ApprovalEndpointBuiltin, SessionID: t.sessionRef.SessionID, ToolCallID: call.ID},
		SessionRef: t.sessionRef,
		Session:    session.CloneSession(t.session),
		RunID:      strings.TrimSpace(t.approval.runID),
		TurnID:     strings.TrimSpace(t.approval.turnID),
		Tool:       t.tool.Definition(),
		Call:       tool.CloneCall(call),
		ModelStep:  cloneModelStepRef(call.ModelStep),
		Approval:   cloneApproval(decision.Approval),
		Metadata:   mapsClone(decision.Metadata),
	}
	if len(request.Approval.Options) == 0 {
		request.Approval.Options = []session.ProtocolApprovalOption{{ID: "allow_once", Name: "Allow once", Kind: "allow_once"}, {ID: "reject_once", Name: "Reject", Kind: "reject_once"}}
	}
	approvedCall := tool.CloneCall(call)
	approvedCall.Metadata = mergeCallMetadata(approvedCall.Metadata, decision)
	started, _ := ctx.Value(taskInvocationStartKey{}).(taskInvocationStart)
	if gate == nil {
		if result, handled, err := submitTaskApproval(ctx, approvedCall, taskApproval{
			owner: t.approval.ctx, request: request, resolve: t.resolveApproval, started: started.time, generation: started.generation,
		}, t.tool); handled {
			return result, err
		}
	}
	resp, err := t.resolveApproval(ctx, request)
	if err != nil {
		return tool.Result{}, err
	}
	if gate != nil {
		return t.resolveMCPApproval(ctx, call, approvedCall, decision, resp, *gate, afterApproved)
	}
	if resp.Approved {
		return afterApproved(ctx, approvedCall)
	}
	return policyDecisionResultWithOutcome(call, t.tool.Definition(), decision, resp), nil
}

func (t policyWrappedTool) resolveMCPApproval(ctx context.Context, call, approvedCall tool.Call, decision policy.Decision, resp agent.ApprovalResponse, grant MCPGrant, afterApproved approvedToolCall) (tool.Result, error) {
	if resp.Outcome != "selected" {
		return policyDecisionResultWithOutcome(call, t.tool.Definition(), decision, resp), nil
	}
	option, meaning, err := approval.ResolveStrictOption(approval.NormalizeProtocolOptions(decision.Approval.Options), resp.OptionID)
	if err != nil || (meaning == approval.OptionDecisionAllow) != resp.Approved {
		return tool.Result{}, fmt.Errorf("invalid MCP approval decision: %w", errors.Join(err, errors.New("option and approval result disagree")))
	}
	if meaning != approval.OptionDecisionAllow {
		return policyDecisionResultWithOutcome(call, t.tool.Definition(), decision, resp), nil
	}
	switch option.ID {
	case "allow_once":
	case "allow_session":
		if err := t.mcpGrants.Grant(grant, "session", t.sessionRef.SessionID); err != nil {
			return tool.Result{}, err
		}
	case "allow_always":
		if err := t.mcpGrants.Grant(grant, "always", ""); err != nil {
			return tool.Result{}, err
		}
	default:
		return tool.Result{}, errors.New("unknown MCP approval scope")
	}
	return afterApproved(ctx, approvedCall)
}

func (t policyWrappedTool) resolveApproval(ctx context.Context, request agent.ApprovalRequest) (agent.ApprovalResponse, error) {
	var resp agent.ApprovalResponse
	var err error
	approvalCall := func(callCtx context.Context) error {
		if t.approval.runtime != nil {
			resp, err = t.approval.runtime.requestDurableApproval(callCtx, request, t.approval.requester)
		} else {
			resp, err = t.approval.requester.RequestApproval(callCtx, request)
		}
		return err
	}
	if t.approval.runtime != nil {
		event := t.approval.runtime.lifecycleEvent(ctx, agent.LifecycleApproval, request.Tool.Name, request.Call.ID)
		err = t.approval.runtime.executeLifecycle(ctx, event, approvalCall)
	} else {
		err = approvalCall(ctx)
	}
	return resp, err
}

func cloneModelStepRef(in *tool.ModelStepRef) *tool.ModelStepRef {
	if in == nil {
		return nil
	}
	out := *in
	out.ID = strings.TrimSpace(out.ID)
	return &out
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

func policyDecisionResult(call tool.Call, def tool.Definition, decision policy.Decision) tool.Result {
	return policyDecisionResultWithOutcome(call, def, decision, agent.ApprovalResponse{})
}

func policyDecisionResultWithOutcome(
	call tool.Call,
	def tool.Definition,
	decision policy.Decision,
	outcome agent.ApprovalResponse,
) tool.Result {
	errorText, systemHint := policyModelFeedback(decision, outcome)
	payload := map[string]any{
		"error":     errorText,
		"tool_name": strings.TrimSpace(def.Name),
	}
	if systemHint != "" {
		payload["system_hint"] = systemHint
	}
	raw, _ := json.Marshal(payload)
	return tool.Result{
		ID:      strings.TrimSpace(call.ID),
		Name:    strings.TrimSpace(def.Name),
		IsError: true,
		Content: []model.Part{model.NewJSONPart(raw)},
	}
}

// policyModelFeedback separates factual error text from one actionable model hint.
func policyModelFeedback(decision policy.Decision, outcome agent.ApprovalResponse) (errorText string, systemHint string) {
	reason := strings.TrimSpace(decision.Reason)
	outcomeReason := strings.TrimSpace(firstNonEmpty(outcome.Reason, outcome.ReviewText))
	approvalResolved := strings.TrimSpace(outcome.Outcome) != "" || strings.TrimSpace(outcome.OptionID) != "" || outcomeReason != ""

	switch {
	case decision.Action == policy.ActionAskApproval && approvalResolved && !outcome.Approved:
		errorText = firstNonEmpty(outcomeReason, "approval denied")
	case decision.Action == policy.ActionAskApproval:
		errorText = firstNonEmpty(reason, "approval required")
		systemHint = "This operation will run only after approval; keep using the same tool rather than shell workarounds."
	case reason != "":
		errorText = reason
		systemHint = "Follow the policy error exactly; do not bypass it with a different command or shell trick."
	default:
		errorText = "tool denied by policy"
		systemHint = "Follow the policy error exactly; do not bypass it with a different command or shell trick."
	}
	return errorText, systemHint
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
