package runtime

import (
	"context"
	"sync/atomic"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
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
		prepareCtx := ctx
		validateCodeMode := snapshot.Validate
		if validateCodeMode == nil {
			validateCodeMode = func(context.Context) (func(), error) {
				return nil, tool.NewError(tool.ErrorCodeStaleRevision, "Code Mode host calls require a dynamic snapshot generation validator")
			}
		}
		prepareCtx = context.WithValue(prepareCtx, codeModeValidationKey{}, validateCodeMode)
		if r.compaction.Enabled {
			authority := &modelRequestAuthority{epoch: snapshot.Revision, refs: 1, release: snapshot.Release, validate: snapshot.Validate, deferredTools: snapshot.DeferredTools, resolveIdle: source}
			if admit := snapshot.Admit; admit != nil {
				authority.admit = func(ctx context.Context, admission agent.ModelRequestAdmission) error {
					admission.TurnID = turnID
					return admit(ctx, admission)
				}
			}
			snapshot.Release = authority.drop
			prepareCtx = context.WithValue(prepareCtx, modelRequestAuthorityKey{}, authority)
		}
		if err := r.prepareAgentSpec(prepareCtx, active, ref, state, &bound, req, runID, turnID, sequence); err != nil {
			if snapshot.Release != nil {
				snapshot.Release()
			}
			return agent.ModelRequestSnapshot{}, err
		}
		snapshot.Model, snapshot.Tools, snapshot.DeferredTools = bound.Model, bound.Tools, bound.DeferredTools
		if catalog, _ := bound.Metadata[codeModeCatalogKey].(string); catalog != "" {
			snapshot.Instructions = append(snapshot.Instructions, model.NewTextPart(catalog))
		}
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
	hasCodeMode := false
	for _, one := range spec.Tools {
		if declaration, ok := one.(*codemode.Tool); ok && declaration.Name == codemode.ToolName {
			hasCodeMode = true
		}
	}
	if hasCodeMode {
		var catalogTools []tool.Tool
		if spec.DeferredTools != nil {
			catalogTools = spec.DeferredTools.Tools()
		}
		catalog, err := r.codeModeCatalog(ctx, ref, catalogTools)
		if err != nil {
			return err
		}
		if spec.Metadata == nil {
			spec.Metadata = map[string]any{}
		}
		spec.Metadata[codeModeCatalogKey] = catalog
	}
	if !hasCodeMode {
		spec.Tools = tool.WithMCPCallTool(spec.Tools)
	}
	modeName, _ := r.policyForName(ctx, r.policyMode(*spec))
	authority, _ := ctx.Value(modelRequestAuthorityKey{}).(*modelRequestAuthority)
	if authority == nil && r.compaction.Enabled {
		authority = &modelRequestAuthority{epoch: "static", deferredTools: spec.DeferredTools, validate: func(context.Context) (func(), error) { return func() {}, nil }, refs: 1}
	}
	spec.Model = r.wrapModelForLifecycle(r.wrapModelForAutoCompaction(ref, spec.Model, authority))
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
	// Bind the concrete declaration only after native tools have received their
	// Runtime injection, and look up targets through the ordinary wrapper chain.
	var allTools []tool.Tool
	targets := func() []tool.Tool {
		out := append([]tool.Tool(nil), allTools...)
		if spec.DeferredTools != nil {
			out = append(out, spec.DeferredTools.Tools()...)
		}
		return out
	}
	var checkBinding func(context.Context, tool.Binding) error
	if source, ok := spec.DeferredTools.(interface {
		CheckToolBinding(context.Context, tool.Binding) error
	}); ok {
		checkBinding = source.CheckToolBinding
	}
	validate, _ := ctx.Value(codeModeValidationKey{}).(func(context.Context) (func(), error))
	for i, one := range spec.Tools {
		if one != nil && tool.IsToolSearchDefinition(one.Definition()) {
			spec.Tools[i] = codeModeDiscoveryTool{base: one, targets: targets}
			continue
		}
		if declaration, ok := one.(*codemode.Tool); ok {
			spec.Tools[i] = runtimeCodeModeTool{tasks: r.tasks, active: session.CloneSession(active), declaration: declaration, sessions: r.sessions, ref: ref, targets: targets, validate: validate, checkBinding: checkBinding}
		}
	}
	allTools = wrap(spec.Tools)
	spec.Tools = allTools
	if hasCodeMode {
		visible := make([]tool.Tool, 0, len(allTools))
		for _, one := range allTools {
			if one != nil && !tool.IsToolSearchDefinition(one.Definition()) && !tool.IsMCPCallDefinition(one.Definition()) {
				visible = append(visible, one)
			}
		}
		spec.Tools = visible
	}
	if spec.DeferredTools != nil {
		spec.DeferredTools = &deferredToolSource{source: spec.DeferredTools, wrap: wrap}
	}
	return nil
}
