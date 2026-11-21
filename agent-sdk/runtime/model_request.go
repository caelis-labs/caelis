package runtime

import (
	"context"
	"fmt"
	"sync/atomic"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/agent-sdk/tool/toolset"
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
		bound.Models = snapshot.Models
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
		snapshot.Models = bound.Models
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
		catalog, err := r.codeModeCatalog(ctx, ref, spec.DeferredTools, spec.Metadata)
		if err != nil {
			return err
		}
		if spec.Metadata == nil {
			spec.Metadata = map[string]any{}
		}
		spec.Metadata[codeModeCatalogKey] = catalog
	}
	modeName := r.policyMode(*spec)
	authority, _ := ctx.Value(modelRequestAuthorityKey{}).(*modelRequestAuthority)
	if authority == nil && r.compaction.Enabled {
		authority = &modelRequestAuthority{epoch: "static", deferredTools: spec.DeferredTools, validate: func(context.Context) (func(), error) { return func() {}, nil }, refs: 1}
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
	spec.Tools = r.wrapToolsForRuntime(active, ref, *spec, runtimeToolContext{
		mode: modeName, approvalMode: string(r.currentApprovalMode(state)),
		approvalRequester: req.ApprovalRequester,
		inputSender:       agent.AgentInputSenderFromContext(ctx),
	})
	wrappingSpec := *spec
	wrap := func(tools []tool.Tool) []tool.Tool {
		bound := wrappingSpec
		bound.Tools = tools
		return r.wrapTurnTools(ctx, active, ref, state, bound, req.ApprovalRequester, runID, turnID, sequence)
	}
	// The model keeps its request's declarations. Code Mode resolves live MCP
	// entries through the same wrapped source, not through those frozen schemas.
	var localTools []tool.Tool
	lookup := func(ctx context.Context, name string) (tool.Tool, bool, error) {
		for _, one := range localTools {
			if one != nil && one.Definition().Name == name {
				return one, true, nil
			}
		}
		if source, ok := spec.DeferredTools.(toolset.NamedSource); ok {
			return source.Lookup(ctx, name)
		}

		return nil, false, nil
	}
	var checkBinding func(context.Context, tool.Binding) error
	if source, ok := spec.DeferredTools.(interface {
		CheckToolBinding(context.Context, tool.Binding) error
	}); ok {
		checkBinding = source.CheckToolBinding
	}
	validate, _ := ctx.Value(codeModeValidationKey{}).(func(context.Context) (func(), error))
	var discovery tool.Tool
	for i, one := range spec.Tools {
		if declaration, ok := one.(*codemode.Tool); ok {
			discovery = codeModeDiscoveryTool{lookup: lookup, checkBinding: checkBinding}
			available := func() []tool.Tool { return codeModeAvailableTools(localTools, spec.DeferredTools) }
			spec.Tools[i] = runtimeCodeModeTool{runtime: r, tasks: r.tasks, active: session.CloneSession(active), declaration: declaration, sessions: r.sessions, ref: ref, availableTools: available, lookup: lookup, validate: validate, checkBinding: checkBinding}
		}
	}
	if discovery != nil {
		visible := make([]tool.Tool, 0, len(spec.Tools))
		for _, candidate := range spec.Tools {
			if candidate != nil && !mcp.IsSearchDefinition(candidate.Definition()) && !mcp.IsCallDefinition(candidate.Definition()) {
				visible = append(visible, candidate)
			}
		}
		spec.Tools = visible
	}
	spec.Tools = wrap(spec.Tools)
	if spec.DeferredTools != nil {
		spec.DeferredTools = &deferredToolSource{source: spec.DeferredTools, wrap: wrap}
	}
	if discovery != nil {
		localTools = make([]tool.Tool, 0, len(spec.Tools)+1)
		for _, one := range spec.Tools {
			if spec.DeferredTools == nil || !mcp.IsModelFunction(one.Definition()) {
				localTools = append(localTools, one)
			}
		}
		localTools = append(localTools, wrap([]tool.Tool{discovery})...)
	}
	return nil
}
