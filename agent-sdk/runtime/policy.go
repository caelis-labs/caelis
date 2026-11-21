package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/internal/sandboxmeta"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

type sandboxRuntimeProvider interface {
	SandboxRuntime() sandbox.Runtime
}

type approvalContext struct {
	ctx       context.Context
	requester agent.ApprovalRequester
	runtime   *Runtime
	runID     string
	turnID    string
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
	normalized := presets.NormalizeModeName(modeName)
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
	return t.RuntimeCall(toolbinding.Token{}, ctx, call, toolbinding.Dispatch{})
}

func (t policyWrappedTool) RuntimeCall(_ toolbinding.Token, ctx context.Context, call tool.Call, dispatch toolbinding.Dispatch) (tool.Result, error) {
	started := taskInvocationStart{time: time.Now()}
	if scope := taskScopeFromContext(t.approval.ctx); scope != nil {
		started.generation = scope.currentGeneration()
	}
	ctx = context.WithValue(ctx, taskInvocationStartKey{}, started)
	defer dispatch.Step.MarkAdmissionComplete()
	if t.policy == nil {
		return tool.Result{}, &policy.ProfileError{Profile: t.mode, Detail: "policy mode is unavailable"}
	}
	if mcp.IsDefinition(t.tool.Definition()) {
		if err := tool.ValidateMCPArguments(t.tool.Definition(), call.Input); err != nil {
			return tool.Result{}, err
		}
	}
	input := policy.ToolContext{
		Session:          t.session,
		State:            session.CloneState(t.state),
		Tool:             t.tool.Definition(),
		Call:             tool.CloneCall(call),
		Sandbox:          t.describeSandbox(),
		SandboxPolicy:    sandbox.ClonePolicySnapshot(t.sandboxPolicy),
		Mode:             t.mode,
		Options:          policy.CloneModeOptions(t.options),
		ReadPathArgument: codeModeReadPathArgument(t.tool),
	}
	if t.approval.runtime != nil {
		input.Authorizations = func(ctx context.Context) (session.AuthorizationEvidence, error) {
			return t.approval.runtime.authorizationEvidence(ctx, t.session)
		}
	}
	decision, err := t.policy.DecideTool(ctx, input)
	if err != nil {
		return tool.Result{}, err
	}
	decision, err = policy.NormalizeDecision(t.mode, decision)
	if err != nil {
		return tool.Result{}, err
	}
	return t.applyPolicyDecision(ctx, call, decision, dispatch)
}

func (t policyWrappedTool) applyPolicyDecision(ctx context.Context, call tool.Call, decision policy.Decision, dispatch toolbinding.Dispatch) (tool.Result, error) {
	switch decision.Action {
	case policy.ActionAllow:
		ctx = context.WithValue(ctx, policyDispatchKey{}, &policyDispatchPermit{runtime: t.approval.runtime, session: t.session, decision: decision})
		dispatch.Step.MarkAdmissionComplete()
		call = tool.CloneCall(call)
		call.Metadata = mergeCallMetadata(call.Metadata, decision)
		return toolbinding.Call(ctx, t.tool, call, dispatch)
	case policy.ActionAskApproval:
		return t.requestApproval(ctx, call, decision, dispatch)
	case policy.ActionDeny:
		dispatch.Step.MarkAdmissionComplete()
		return policyDecisionResult(t.tool.Definition(), decision)
	default:
		return tool.Result{}, &policy.DecisionError{Mode: t.mode, Detail: "unhandled normalized decision"}
	}
}

func (t policyWrappedTool) requestApproval(
	ctx context.Context,
	call tool.Call,
	decision policy.Decision,
	dispatch toolbinding.Dispatch,
) (tool.Result, error) {
	if decision.Approval == nil || (t.approval.requester == nil && t.approval.runtime == nil) {
		return policyDecisionResult(t.tool.Definition(), decision)
	}
	request := t.approvalRequest(ctx, call, decision, dispatch)
	approvedCall := tool.CloneCall(call)
	approvedCall.Metadata = mergeCallMetadata(approvedCall.Metadata, decision)
	started, _ := ctx.Value(taskInvocationStartKey{}).(taskInvocationStart)
	// Task producer continuation remains the sole execution owner. Generic
	// source consent does not introduce another command submission path.
	if decision.Authorization == nil {
		if result, handled, err := submitTaskApproval(ctx, approvedCall, taskApproval{owner: t.approval.ctx, request: request, resolve: t.resolveApproval, started: started.time, generation: started.generation}, t.tool); handled {
			return result, err
		}
	}
	response, err := t.resolvePolicyApproval(ctx, request)
	if err != nil {
		return tool.Result{}, err
	}
	if response.Approved {
		ctx = context.WithValue(ctx, policyDispatchKey{}, &policyDispatchPermit{runtime: t.approval.runtime, session: t.session, decision: decision, optionID: response.OptionID})
		return toolbinding.Call(ctx, t.tool, approvedCall, dispatch)
	}
	return policyDecisionResultWithOutcome(t.tool.Definition(), decision, response)
}

func (t policyWrappedTool) approvalRequest(ctx context.Context, call tool.Call, decision policy.Decision, dispatch toolbinding.Dispatch) agent.ApprovalRequest {
	request := agent.ApprovalRequest{
		Authorization:  session.CloneApprovalAuthorization(decision.Authorization),
		Origin:         &agent.ApprovalOrigin{WorkingDirectory: t.session.CWD, Role: agent.ApprovalRoleMain, Endpoint: agent.ApprovalEndpointBuiltin, SessionID: t.sessionRef.SessionID, ToolCallID: call.ID},
		SessionRef:     t.sessionRef,
		Session:        session.CloneSession(t.session),
		RunID:          strings.TrimSpace(t.approval.runID),
		TurnID:         strings.TrimSpace(t.approval.turnID),
		Tool:           t.tool.Definition(),
		Call:           tool.CloneCall(call),
		Execution:      tool.InvocationFromContext(ctx),
		Parent:         tool.ParentFromContext(ctx),
		ProducingModel: dispatch.Model,
		ModelStep:      agent.CloneModelStepRef(dispatch.Step),
		Approval:       cloneApproval(decision.Approval),
		Metadata:       session.CloneState(decision.Metadata),
	}
	if len(request.Approval.Options) == 0 {
		request.Approval.Options = []session.ProtocolApprovalOption{{ID: "allow_once", Name: "Allow once", Kind: "allow_once"}, {ID: "reject_once", Name: "Reject", Kind: "reject_once"}}
	}
	return request
}

func (t policyWrappedTool) resolveApproval(ctx context.Context, request agent.ApprovalRequest) (agent.ApprovalResponse, error) {
	var resp agent.ApprovalResponse
	var err error
	approvalCall := func(callCtx context.Context) error {
		if t.approval.runtime != nil {
			resp, err = t.approval.runtime.requestDurableApproval(callCtx, request, t.approval.requester)
		} else {
			permission := cloneApproval(request.Approval)
			resp, err = t.approval.requester.RequestApproval(callCtx, request)
			if err == nil {
				resp, err = approval.ResolveNativeResponse(permission, resp)
			}
		}
		return err
	}
	if t.approval.runtime != nil {
		event := t.approval.runtime.lifecycleEvent(ctx, agent.LifecycleApproval, request.Tool.Name, request.Call.ID)
		err = t.approval.runtime.traceOperation(ctx, event, approvalCall)
	} else {
		err = approvalCall(ctx)
	}
	return resp, err
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
		out[sandboxmeta.ConstraintsKey] = decision.Constraints
	}
	if decision.Metadata != nil {
		out["policy_metadata"] = decision.Metadata
	}
	return out
}

func policyDecisionResult(def tool.Definition, decision policy.Decision) (tool.Result, error) {
	return policyDecisionResultWithOutcome(def, decision, agent.ApprovalResponse{})
}

func policyDecisionResultWithOutcome(
	def tool.Definition,
	decision policy.Decision,
	outcome agent.ApprovalResponse,
) (tool.Result, error) {
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
		Content: []tool.ContentBlock{tool.StructuredContent{Value: raw}},
	}, tool.FailedReply(errorText)
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
