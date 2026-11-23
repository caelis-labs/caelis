package runtime

import (
	"context"
	"strings"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

type approvalContext struct {
	ctx       context.Context
	requester approval.Requester
	runtime   *Runtime
	runID     string
	turnID    string
}

type policyAdmission struct {
	mode          string
	policy        policy.Mode
	session       session.Session
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

func (m rejectedPolicyMode) Name() string { return strings.TrimSpace(m.name) }

func (m rejectedPolicyMode) DecideTool(context.Context, policy.ToolContext) (policy.Decision, error) {
	return policy.Decision{}, m.err
}

// bindToolDispatch installs policy at the last stage before guarded dispatch.
func (r *Runtime) bindToolDispatch(active session.Session, ref session.SessionRef, state map[string]any, spec agent.AgentSpec, approval approvalContext) []tool.Tool {
	if len(spec.Tools) == 0 {
		return spec.Tools
	}
	name := r.policyMode(spec)
	var admission policyAdmission
	if name != "" {
		admission.mode, admission.policy = r.policyForName(approval.ctx, name)
		admission.session = session.CloneSession(active)
		admission.state = session.CloneState(state)
		admission.options = modeOptionsFromSession(active, spec)
		admission.sandboxPolicy = sandbox.ClonePolicySnapshot(r.sandboxPolicy)
		admission.approval = approval
	}
	out := make([]tool.Tool, 0, len(spec.Tools))
	for _, target := range spec.Tools {
		if target == nil {
			continue
		}
		bound := dispatchTool{base: target, sessions: r.sessions, sessionRef: ref, writes: &r.sessionWrites}
		if name != "" {
			entry := admission
			entry.tool = target
			bound.before = entry.beforeTool
		}
		out = append(out, bound)
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
	if r == nil || r.hooks.ToolPolicy == nil {
		return normalized, nil, &policy.ProfileError{
			Profile: normalized,
			Detail:  "policy registry is unavailable",
		}
	}
	mode, ok, err := r.hooks.ToolPolicy.Lookup(ctx, normalized)
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

func (t policyAdmission) beforeTool(ctx context.Context, call tool.Call, dispatch toolbinding.Dispatch, next toolDispatch) (tool.Result, error) {
	started := taskInvocationStart{time: time.Now()}
	if scope := taskScopeFromContext(t.approval.ctx); scope != nil {
		started.generation = scope.currentGeneration()
	}
	ctx = context.WithValue(ctx, taskInvocationStartKey{}, started)
	defer dispatch.Step.MarkAdmissionComplete()
	if mcp.IsDefinition(t.tool.Definition()) {
		if err := tool.ValidateMCPArguments(t.tool.Definition(), call.Input); err != nil {
			return tool.Result{}, err
		}
	}
	input := policy.ToolContext{
		Session:          t.session,
		State:            t.state,
		Tool:             t.tool.Definition(),
		Call:             call,
		SandboxPolicy:    t.sandboxPolicy,
		Mode:             t.mode,
		Options:          t.options,
		ReadPathArgument: codeModeReadPathArgument(t.tool),
	}
	if t.approval.runtime != nil {
		input.Authorizations = func(ctx context.Context) (session.AuthorizationEvidence, error) {
			return t.approval.runtime.authorizationEvidence(ctx, t.session)
		}
	}
	return policy.BeforeTool(ctx, t.policy, input, func(d policy.Decision) (tool.Result, error) {
		dispatch.Step.MarkAdmissionComplete()
		return t.dispatchAllowed(ctx, d.EffectiveCall(call), d, "", dispatch, next)
	}, func(d policy.Decision) (tool.Result, error) {
		return t.requestApproval(ctx, call, d, dispatch, next)
	})
}

func (t policyAdmission) dispatchAllowed(ctx context.Context, call tool.Call, d policy.Decision, optionID string, dispatch toolbinding.Dispatch, next toolDispatch) (tool.Result, error) {
	permit := &policyDispatchPermit{runtime: t.approval.runtime, session: t.session, decision: d, optionID: optionID}
	return next(context.WithValue(ctx, policyDispatchKey{}, permit), call, dispatch)
}

func (t policyAdmission) requestApproval(
	ctx context.Context,
	call tool.Call,
	decision policy.Decision,
	dispatch toolbinding.Dispatch,
	next toolDispatch,
) (tool.Result, error) {
	if decision.Approval == nil || (t.approval.requester == nil && t.approval.runtime == nil) {
		return decision.Refusal(t.tool.Definition(), approval.Response{})
	}
	request := decision.PermissionRequest(ctx, t.session, t.approval.runID, t.approval.turnID, t.tool.Definition(), call, dispatch.Model, dispatch.Step)
	approvedCall := decision.EffectiveCall(call)
	started, _ := ctx.Value(taskInvocationStartKey{}).(taskInvocationStart)
	// Task producer continuation remains the sole execution owner. Generic
	// source consent does not introduce another command submission path.
	if decision.Authorization == nil {
		pending := taskApproval{owner: t.approval.ctx, request: request, resolve: t.resolveApproval, started: started.time, generation: started.generation}
		resume := func(ctx context.Context, call tool.Call, optionID string) (tool.Result, error) {
			return t.dispatchAllowed(ctx, call, decision, optionID, toolbinding.Dispatch{}, next)
		}
		if result, handled, err := submitTaskApproval(ctx, approvedCall, pending, t.tool, resume); handled {
			return result, err
		}
	}
	response, err := t.resolvePolicyApproval(ctx, request)
	if err != nil {
		return tool.Result{}, err
	}
	if response.Approved {
		return t.dispatchAllowed(ctx, approvedCall, decision, response.OptionID, dispatch, next)
	}
	return decision.Refusal(t.tool.Definition(), response)
}

func (t policyAdmission) resolveApproval(ctx context.Context, request approval.Request) (approval.Response, error) {
	var resp approval.Response
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
