package gatewayapp

import (
	"context"
	"errors"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/internal/sandboxrouter"
)

// assembleObservationSnapshot gives idle Application projections the declared
// profile without assembling a sandbox, provider, MCP service or execution
// Runtime. Ordinary Session projection keeps its existing assembly owner.
func (a *workspaceConfigAssembler) assembleObservationSnapshot(ctx context.Context, active session.Session, sessions session.Service) (*sessionRuntimeInstance, error) {
	if !sessionvisibility.IsApplicationSession(active) {
		return a.assembleSnapshot(ctx, active, sessionRuntimeActivity{}, sessions)
	}
	binding, err := a.deps.authorities.applications.BindingForSession(ctx, active.SessionID)
	if err != nil {
		return nil, err
	}
	workspace, err := canonicalSessionWorkspace(active)
	if err != nil {
		return nil, err
	}
	_, lookup, err := a.loadRuntimeModelSnapshot(ctx, active)
	if err != nil {
		return nil, err
	}
	mode := applicationPolicyMode(binding.Profile)
	backend := "none"
	if binding.Profile.Execution == "workspace-write" {
		selected := sandbox.Backend("")
		if binding.Profile.Permissions.Mode == "danger-full-access" {
			selected = sandbox.BackendHost
		}
		route, err := sandboxrouter.Current(selected)
		if err != nil {
			return nil, err
		}
		backend = string(route.Backend)
	}
	return &sessionRuntimeInstance{runtimeComposition: runtimeComposition{
		authorities: a.deps.authorities, sessions: sessions, workspace: workspace, lookup: lookup,
		activation: &sessionRuntimeActivation{modelCatalog: a.deps.modelCatalog, sessionRef: active.SessionRef},
		activeRuntime: stackRuntimeConfig{ContextWindow: a.deps.processConfig.snapshot().runtime.ContextWindow,
			ApprovalMode: application.EffectiveApprovalMode(binding.Profile), PolicyProfile: mode},
		sandbox: SandboxConfig{RequestedType: backend},
	}}, nil
}

func (s *runtimeComposition) applicationRuntimeState(ctx context.Context, binding application.Binding) (SessionRuntimeState, error) {
	configuration, err := s.authorities.applications.Configuration(ctx, binding.Scope, binding.SessionID)
	if err != nil {
		return SessionRuntimeState{}, err
	}
	doc, err := s.authorities.store.LoadContext(ctx)
	if err != nil {
		return SessionRuntimeState{}, err
	}
	lookup, err := newModelLookupFromDocument(doc, 0)
	if err != nil {
		return SessionRuntimeState{}, err
	}
	configured, err := applicationModelConfig(lookup, configuration.Profile)
	if err != nil {
		return SessionRuntimeState{}, err
	}
	mode := applicationPolicyMode(binding.Profile)
	return SessionRuntimeState{ModelID: configured.ID, ModelAlias: configured.Alias,
		ReasoningEffort: configured.ReasoningEffort, FastMode: configuration.Profile.ServiceTier == "priority",
		SessionMode: application.EffectiveApprovalMode(binding.Profile), PolicyProfile: mode}, nil
}

// applicationCompletionSkills shares the Skill catalog scanner, including its
// exact explicit selections and damaged-item behavior, without starting MCP.
func (s *runtimeComposition) applicationCompletionSkills() (skill.Catalog, bool) {
	if s == nil || s.activation == nil || s.authorities.applications == nil {
		return skill.Catalog{}, false
	}
	ctx := context.Background()
	binding, err := s.authorities.applications.BindingForSession(ctx, s.activation.sessionRef.SessionID)
	if errors.Is(err, application.ErrNotFound) {
		return skill.Catalog{}, false
	}
	if err != nil {
		return skill.Catalog{}, true
	}
	configuration, err := s.authorities.applications.Configuration(ctx, binding.Scope, binding.SessionID)
	if err != nil {
		return skill.Catalog{}, true
	}
	catalog, _, _ := assembleApplicationSkills(configuration.Profile, false)
	return catalog, true
}
