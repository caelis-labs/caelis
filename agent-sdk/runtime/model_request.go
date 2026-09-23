package runtime

import (
	"context"
	"sync/atomic"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// wrapModelRequestResolver admits changing model-facing values through the same
// Runtime owners as the static AgentSpec, retaining the Turn's execution identity
// and authority. No embedding configuration or revision semantics belong here.
func (r *Runtime) wrapModelRequestResolver(ctx context.Context, active session.Session, ref session.SessionRef, state map[string]any, spec agent.AgentSpec, req agent.RunRequest, runID, turnID string, sequence *atomic.Uint64) agent.ModelRequestResolver {
	source := spec.ResolveModelRequest
	return func(callCtx context.Context) (agent.ModelRequestSnapshot, error) {
		snapshot, err := source(callCtx)
		if err != nil {
			return agent.ModelRequestSnapshot{}, err
		}
		snapshot = agent.CloneModelRequestSnapshot(snapshot)
		// RunRequest overrides only presentation/output choices. The dynamic
		// snapshot owns service tier, including an explicit empty/default tier.
		overrides := req.Request.WithDefaults(spec.Request)
		overrides.ServiceTier = ""
		snapshot.Request = overrides.WithDefaults(snapshot.Request)
		bound := cloneAgentSpec(spec)
		bound.Model, bound.Tools, bound.Request = snapshot.Model, snapshot.Tools, snapshot.Request
		bound.DeferredTools = snapshot.DeferredTools
		bound.ResolveModelRequest = nil
		if err := r.prepareAgentSpec(ctx, active, ref, state, &bound, req, runID, turnID, sequence); err != nil {
			return agent.ModelRequestSnapshot{}, err
		}
		snapshot.Model, snapshot.Tools, snapshot.DeferredTools = bound.Model, bound.Tools, bound.DeferredTools
		if admit := snapshot.Admit; admit != nil {
			snapshot.Admit = func(ctx context.Context, admission agent.ModelRequestAdmission) error {
				admission.TurnID = turnID
				return admit(ctx, admission)
			}
		}
		return snapshot, nil
	}
}

func (r *Runtime) prepareAgentSpec(ctx context.Context, active session.Session, ref session.SessionRef, state map[string]any, spec *agent.AgentSpec, req agent.RunRequest, runID, turnID string, sequence *atomic.Uint64) error {
	if err := validateAgentSpecCapabilities(spec.Model, spec.Tools, spec.Request.OutputSpec(), spec.Request.StreamEnabled(false), spec.RequiredModelCapabilities); err != nil {
		return err
	}
	modeName, _ := r.policyForName(ctx, r.policyMode(*spec))
	spec.Model = r.wrapModelForLifecycle(r.wrapModelForAutoCompaction(ref, spec.Model))
	spec.Tools = r.wrapToolsForRuntime(active, ref, *spec, runtimeToolContext{
		mode: modeName, approvalMode: string(r.currentApprovalMode(state)),
		approvalRequester: req.ApprovalRequester, runID: runID, turnID: turnID,
		inputSender: agent.AgentInputSenderFromContext(ctx),
	})
	wrappingSpec := *spec
	wrap := func(tools []tool.Tool) []tool.Tool {
		bound := wrappingSpec
		bound.Tools = tools
		return r.wrapTurnTools(ctx, active, ref, state, bound, req.ApprovalRequester, runID, turnID, sequence)
	}
	spec.Tools = wrap(spec.Tools)
	if spec.DeferredTools != nil {
		spec.DeferredTools = &deferredToolSource{source: spec.DeferredTools, wrap: wrap}
	}
	return nil
}
