package gatewayapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/mcpconfig"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/codexauth"
	"github.com/caelis-labs/caelis/control/modelconfig/grokauth"
	"github.com/caelis-labs/caelis/control/modelconfig/providerusage"
	"github.com/caelis-labs/caelis/control/workspacetrust"
	controller "github.com/caelis-labs/caelis/internal/acpagentbridge/controller"
)

type ModelService struct {
	composition *runtimeComposition
}

type AgentService struct {
	composition *runtimeComposition
}

type SkillService struct {
	composition *runtimeComposition
}

type StatusService struct {
	composition *runtimeComposition
}

// PresentationSource is the protocol-neutral read model used behind the
// AppServer Presentation capability. Product writes belong to the focused
// Configuration command service; ACP fallback providers are normalized at
// this Host-private boundary and never flow into the local adapter.
type PresentationSource interface {
	SessionModes(context.Context, session.Session) (*appserver.PresentationModeState, error)
	SessionConfigOptions(context.Context, session.Session) ([]appserver.PresentationConfigOption, error)
	SessionModels(context.Context, session.Session) (*appserver.PresentationModelState, error)
	AvailableCommands(context.Context, string) ([]appserver.PresentationCommand, error)
	PromptCapabilities(context.Context) (appserver.PresentationCapabilities, error)
}

func (s *runtimeComposition) Models() ModelService {
	return ModelService{composition: s}
}

func (s *runtimeComposition) Agents() AgentService {
	return AgentService{composition: s}
}

func (s *runtimeComposition) Skills() SkillService {
	return SkillService{composition: s}
}

func (s *runtimeComposition) Status() StatusService {
	return StatusService{composition: s}
}

// ControlStatus returns the focused read-only status projection used by
// AppServer composition.
func (s *Stack) ControlStatus() StatusService {
	if s == nil {
		return StatusService{}
	}
	return s.composition.Status()
}

func (s *Stack) PresentationSource(modes presentationModeReader, useFallbackModes bool, configs presentationConfigReader) PresentationSource {
	if s == nil {
		return newGatewayPresentationSource(gatewayPresentationSourceDeps{}, modes, useFallbackModes, configs)
	}
	composition := &s.composition
	status := composition.Status()
	agents := composition.Agents()
	bindingStatus := s.AgentBindings()
	lookup := composition.lookup
	return newGatewayPresentationSource(gatewayPresentationSourceDeps{
		sessions:         composition.sessions,
		appName:          composition.authorities.appName,
		userID:           composition.authorities.userID,
		fullAccessModeFn: func() bool { return composition.processSecurityPosture().FullAccessMode },
		runtimeStateFn:   status.SessionRuntimeState,
		modelSnapshotFn: func() persistedModelConfig {
			if lookup == nil {
				return persistedModelConfig{}
			}
			return lookup.Snapshot()
		},
		bindingStatusFn:    bindingStatus.AgentBindingStatus,
		controllerStatusFn: agents.ControllerStatus,
		listAgentsFn:       agents.List,
	}, modes, useFallbackModes, configs)
}

func (s ModelService) ListAliases(ctx context.Context, ref session.SessionRef) ([]string, error) {
	return s.composition.ListModelAliases(ctx, ref)
}

func (s ModelService) ListChoices(ctx context.Context, ref session.SessionRef) ([]ModelChoice, error) {
	return s.composition.ListModelChoices(ctx, ref)
}

func (s ModelService) HasReusableAuth(ctx context.Context, provider string, baseURL string) bool {
	return s.composition.HasReusableProviderAuth(ctx, provider, baseURL)
}

func (s ModelService) DefaultAlias() string {
	return s.composition.DefaultModelAlias()
}

func (s ModelService) DefaultEffort() string {
	return s.composition.DefaultModelEffort()
}

// EffectiveAlias returns the model selected for new work in this Host process.
// It may differ from the persisted Host default when startup flags provide a
// process-scoped ModelProfile override.
func (s ModelService) EffectiveAlias() string {
	return s.composition.EffectiveModelAlias()
}

// EffectiveEffort returns the reasoning effort selected for new work in this
// Host process. It follows the same startup-override semantics as
// EffectiveAlias.
func (s ModelService) EffectiveEffort() string {
	return s.composition.EffectiveModelEffort()
}

func (s ModelService) Config(alias string) (ModelConfig, bool) {
	return s.composition.ModelConfig(alias)
}

// authenticateModelProvider runs the Control-owned interactive authentication
// effect behind the recoverable Host configuration command. It is deliberately
// not exposed through ModelService: discovery and completion are read-only.
func (s *controlCommandBackend) authenticateModelProvider(ctx context.Context, req modelconfig.AuthenticateRequest) error {
	if s == nil {
		return fmt.Errorf("gatewayapp: control command backend is unavailable")
	}
	template, ok := modelconfig.LookupProvider(req.Provider)
	if ok && template.AuthFlow == modelconfig.AuthFlowCodexOAuth {
		if s.composition == nil || s.composition.authorities.codexAuth == nil {
			return fmt.Errorf("gatewayapp: codex authentication is unavailable")
		}
		return s.composition.authorities.codexAuth.EnsureAuthenticated(ctx, codexauth.LoginOptions{
			HTTPClient:      req.HTTPClient,
			OpenBrowser:     req.OpenBrowser,
			CallbackTimeout: req.CallbackTimeout,
		})
	}
	if ok && template.AuthFlow == modelconfig.AuthFlowGrokOAuth {
		if s.composition == nil || s.composition.authorities.grokAuth == nil {
			return fmt.Errorf("gatewayapp: grok authentication is unavailable")
		}
		return s.composition.authorities.grokAuth.EnsureAuthenticated(ctx, grokauth.LoginOptions{
			HTTPClient:      req.HTTPClient,
			OpenBrowser:     req.OpenBrowser,
			CallbackTimeout: req.CallbackTimeout,
		})
	}
	return modelconfig.AuthenticateProvider(ctx, req)
}

func (s ModelService) UsageSnapshot(ctx context.Context, ref session.SessionRef, modelAlias string) (compact.UsageSnapshot, error) {
	return s.composition.SessionUsageSnapshot(ctx, ref, modelAlias)
}

// ProviderUsage returns the latest cached account-level subscription windows
// for the selected model's provider and schedules a bounded asynchronous
// refresh. It never waits for the provider account API. found=false means the
// provider has no usage adapter or the model is not backed by a subscription
// credential.
func (s ModelService) ProviderUsage(ctx context.Context, modelAlias string) (providerusage.Snapshot, bool, error) {
	if s.composition == nil || s.composition.authorities.providerUsage == nil {
		return providerusage.Snapshot{}, false, nil
	}
	config, ok := s.composition.ModelConfig(modelAlias)
	if !ok {
		return providerusage.Snapshot{}, false, nil
	}
	switch config.CredentialRef {
	case modelconfig.CodexOAuthCredentialRef, modelconfig.GrokOAuthCredentialRef:
	default:
		return providerusage.Snapshot{}, false, nil
	}
	return s.composition.authorities.providerUsage.Query(ctx, config.Provider)
}

func (s AgentService) ControllerStatus(ctx context.Context, ref session.SessionRef) (controller.ControllerStatus, bool, error) {
	return s.composition.ACPControllerStatus(ctx, ref)
}

func (s AgentService) DisconnectCandidates(ctx context.Context) ([]controlagents.DisconnectCandidate, error) {
	return s.composition.DisconnectCandidates(ctx)
}

// DisconnectCandidatesSnapshot returns the revision-bound external Agent
// roster used by Host-scoped AppServer reads.
func (s AgentService) DisconnectCandidatesSnapshot(ctx context.Context) (appserver.DisconnectCandidatesSnapshot, error) {
	return s.composition.DisconnectCandidatesSnapshot(ctx)
}

func (s AgentService) List() []ACPAgentInfo {
	return s.composition.ListACPAgents()
}

func (s SkillService) Discover(ctx context.Context, workspaceDir string) ([]SkillMeta, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if s.composition == nil {
		return DiscoverSkillMeta(nil, workspaceDir)
	}
	processRuntime := s.composition.runtimeProcessSnapshot().runtime
	s.composition.mu.RLock()
	pluginSkills := skill.ClonePluginBundles(s.composition.activeRuntime.PluginSkills)
	defaultWorkspace := s.composition.workspace.CWD
	s.composition.mu.RUnlock()
	if strings.TrimSpace(workspaceDir) == "" {
		workspaceDir = defaultWorkspace
	}
	return DiscoverSkillMetaRequest(skill.DiscoverRequest{
		Dirs:          stackSkillDiscoveryDirs(workspaceDir, processRuntime.SkillDirs),
		WorkspaceDir:  workspaceDir,
		PluginBundles: pluginSkills,
	})
}

// Snapshot returns the skill catalog captured when the current runtime prompt
// was assembled. It is stable for the runtime lifetime.
func (s SkillService) Snapshot() skill.Catalog {
	if s.composition == nil {
		return skill.Catalog{}
	}
	return s.composition.skillCatalogSnapshot()
}

func (s *runtimeComposition) skillCatalogSnapshot() skill.Catalog {
	if s == nil {
		return skill.Catalog{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeRuntime.SkillCatalog
}

func (s StatusService) Doctor(ctx context.Context, req DoctorRequest) (DoctorReport, error) {
	return s.composition.Doctor(ctx, req)
}

// ConfigurationRevision returns the canonical Host configuration revision
// observed by this Runtime's focused status projection.
func (s StatusService) ConfigurationRevision(ctx context.Context) (uint64, error) {
	return s.composition.ConfigurationRevision(ctx)
}

// WorkspaceTrust returns the exact persisted trust decision for workspace.
func (s StatusService) WorkspaceTrust(ctx context.Context, workspace string) (workspacetrust.Level, error) {
	if s.composition == nil || s.composition.authorities.store == nil {
		return workspacetrust.Unknown, fmt.Errorf("gatewayapp: app config store unavailable")
	}
	doc, err := s.composition.authorities.store.LoadContext(contextOrBackground(ctx))
	if err != nil {
		return workspacetrust.Unknown, err
	}
	return workspacetrust.Lookup(doc.WorkspaceTrust, workspace), nil
}

// ProjectMCPConfigurationPresent reports whether the exact workspace contains
// a supported project MCP overlay without reading its untrusted content.
func (s StatusService) ProjectMCPConfigurationPresent(ctx context.Context, workspace string) (bool, error) {
	if err := contextOrBackground(ctx).Err(); err != nil {
		return false, err
	}
	return mcpconfig.ProjectOverlayPresent(workspace)
}

func (s StatusService) Sandbox() SandboxStatus {
	return s.composition.SandboxStatus()
}

// SandboxForWorkspace projects sandbox state for one explicitly addressed
// workspace without substituting the Host startup workspace.
func (s StatusService) SandboxForWorkspace(workspace session.WorkspaceRef) SandboxStatus {
	return s.composition.SandboxStatusForWorkspace(workspace)
}

// DoctorForWorkspace returns diagnostics for one explicitly addressed
// workspace without retaining the concrete Host Stack.
func (s StatusService) DoctorForWorkspace(ctx context.Context, workspace session.WorkspaceRef, req DoctorRequest) (DoctorReport, error) {
	return s.composition.DoctorForWorkspace(ctx, workspace, req)
}

func (s StatusService) SessionRuntimeState(ctx context.Context, ref session.SessionRef) (SessionRuntimeState, error) {
	return s.composition.SessionRuntimeState(ctx, ref)
}

// SessionTasks returns the durable Task records owned by one Session for
// focused status accounting.
func (s StatusService) SessionTasks(ctx context.Context, ref session.SessionRef) ([]*taskapi.Entry, error) {
	if s.composition == nil || s.composition.authorities.taskStore == nil {
		return nil, nil
	}
	return s.composition.authorities.taskStore.ListSession(ctx, session.NormalizeSessionRef(ref))
}
