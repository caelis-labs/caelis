package runtime

import (
	"context"
	"fmt"
	"sync/atomic"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func (r *Runtime) resolveAgent(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	state map[string]any,
	runID string,
	turnID string,
	req agent.RunRequest,
	toolStepSequence *atomic.Uint64,
) (agent.Agent, error) {
	if req.Agent != nil {
		return req.Agent, nil
	}
	spec := cloneAgentSpec(req.AgentSpec)
	spec.Request = req.Request.WithDefaults(spec.Request)
	prepare := func(bound *agent.AgentSpec, snapshot *agent.ModelRequestSnapshot) error {
		if err := r.prepareRequestModels(ref, bound, snapshot, req.AgentSpec.ResolveModelRequest); err != nil {
			return err
		}
		// The deferred source can outlive this preparation. Capture this request's
		// policy and the Turn's occurrence sequence for both direct and deferred tools.
		wrappingSpec := *bound
		wrap := func(tools []tool.Tool) []tool.Tool {
			toolSpec := wrappingSpec
			toolSpec.Tools = tools
			return r.wrapTurnTools(ctx, activeSession, ref, state, toolSpec, req.ApprovalRequester, runID, turnID, toolStepSequence)
		}
		bound.Tools = r.wrapToolsForRuntime(activeSession, ref, *bound, runtimeToolContext{
			mode: r.policyMode(*bound), approvalMode: string(r.currentApprovalMode(state)),
			approvalRequester: req.ApprovalRequester, inputSender: agent.AgentInputSenderFromContext(ctx),
		})
		return r.bindRequestTools(ctx, activeSession, ref, bound, snapshot, wrap)
	}
	if spec.ResolveModelRequest != nil {
		spec.ResolveModelRequest = bindModelRequests(spec, turnID, prepare)
	} else if err := prepare(&spec, nil); err != nil {
		return nil, err
	}

	return r.agentFactory.NewAgent(ctx, spec)
}

// bindModelRequests prepares each unsent request with the same bindings as a static spec.
func bindModelRequests(spec agent.AgentSpec, turnID string, prepare func(*agent.AgentSpec, *agent.ModelRequestSnapshot) error) agent.ModelRequestResolver {
	source := spec.ResolveModelRequest
	return func(callCtx context.Context) (agent.ModelRequestSnapshot, error) {
		snapshot, err := source(callCtx)
		if err != nil {
			return agent.ModelRequestSnapshot{}, err
		}
		snapshot = agent.CloneModelRequestSnapshot(snapshot)
		// RunRequest overrides only presentation/output choices. The dynamic
		// snapshot owns service tier, including an explicit empty/default tier.
		overrides := spec.Request
		overrides.ServiceTier = ""
		snapshot.Request = overrides.WithDefaults(snapshot.Request)
		bound := cloneAgentSpec(spec)
		bound.Models = snapshot.Models
		bound.Model, bound.Tools, bound.Request = snapshot.Model, snapshot.Tools, snapshot.Request
		bound.DeferredTools = snapshot.DeferredTools
		bound.ResolveModelRequest = nil
		if admit := snapshot.Admit; admit != nil {
			snapshot.Admit = func(ctx context.Context, admission agent.ModelRequestAdmission) error {
				admission.TurnID = turnID
				return admit(ctx, admission)
			}
		}
		if err := prepare(&bound, &snapshot); err != nil {
			if snapshot.Release != nil {
				snapshot.Release()
			}
			return agent.ModelRequestSnapshot{}, err
		}
		snapshot.Models = bound.Models
		snapshot.Model, snapshot.Tools, snapshot.DeferredTools = bound.Model, bound.Tools, bound.DeferredTools
		return snapshot, nil
	}
}

func (r *Runtime) prepareRequestModels(ref session.SessionRef, spec *agent.AgentSpec, snapshot *agent.ModelRequestSnapshot, resolveIdle agent.ModelRequestResolver) error {
	if err := validateAgentSpecCapabilities(spec.Model, spec.Tools, spec.Request.OutputSpec(), spec.Request.StreamEnabled(false), spec.RequiredModelCapabilities); err != nil {
		return err
	}
	var authority *modelRequestAuthority
	if r.compaction.Enabled {
		if snapshot == nil {
			authority = &modelRequestAuthority{epoch: "static", refs: 1, deferredTools: spec.DeferredTools, validate: func(context.Context) (func(), error) { return func() {}, nil }}
		} else {
			authority = &modelRequestAuthority{epoch: snapshot.Revision, refs: 1, deferredTools: spec.DeferredTools,
				admit: snapshot.Admit, validate: snapshot.Validate, release: snapshot.Release, resolveIdle: resolveIdle}
			snapshot.Release = authority.drop
		}
	}
	for key, candidate := range spec.Models {
		if candidate == nil {
			return fmt.Errorf("model candidate %q is nil", key)
		}
		if err := validateAgentSpecCapabilities(candidate, spec.Tools, spec.Request.OutputSpec(), spec.Request.StreamEnabled(false), spec.RequiredModelCapabilities); err != nil {
			return err
		}
		spec.Models[key] = r.wrapModelForAutoCompaction(ref, candidate, authority)
	}
	spec.Model = r.wrapModelForAutoCompaction(ref, spec.Model, authority)
	return nil
}
