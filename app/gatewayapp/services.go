package gatewayapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/codexauth"
	"github.com/caelis-labs/caelis/control/modelconfig/grokauth"
	"github.com/caelis-labs/caelis/control/modelconfig/providerusage"
	controller "github.com/caelis-labs/caelis/internal/acpagentbridge/controller"
	"github.com/caelis-labs/caelis/protocol/acp"
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
	composition        *runtimeComposition
	preflightSandboxFn func(context.Context, bool) (SandboxStatus, error)
}

// ACPPresentationService is the read-only ACP-shaped projection used behind
// the AppServer Presentation capability. Product writes belong to the focused
// Configuration command service.
type ACPPresentationService interface {
	SessionModes(context.Context, session.Session) (*acp.SessionModeState, error)
	SessionConfigOptions(context.Context, session.Session) ([]acp.SessionConfigOption, error)
	SessionModels(context.Context, session.Session) (*acp.SessionModelState, error)
	AvailableCommands(context.Context, string) ([]acp.AvailableCommand, error)
	PromptCapabilities(context.Context) (acp.PromptCapabilities, error)
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

// Status adds process-only bootstrap diagnostics when the projection is
// requested from the Host. Detached Session Runtimes expose the same read
// snapshot without Host lifecycle mutation seams.
func (s *Stack) Status() StatusService {
	if s == nil {
		return StatusService{}
	}
	return StatusService{composition: &s.composition, preflightSandboxFn: s.PreflightSandbox}
}

// ControlStatus returns the focused read-only status projection used by
// AppServer composition. Host bootstrap preflight remains on Status().
func (s *Stack) ControlStatus() StatusService {
	if s == nil {
		return StatusService{}
	}
	return s.composition.Status()
}

func (s *Stack) ACPSurface(modes acp.ModeProvider, useFallbackModes bool, configs acp.ConfigProvider) ACPPresentationService {
	if s == nil {
		return newGatewayACPSurface(gatewayACPSurfaceDeps{}, modes, useFallbackModes, configs)
	}
	composition := &s.composition
	status := composition.Status()
	agents := composition.Agents()
	bindingStatus := s.AgentBindings()
	lookup := composition.lookup
	return newGatewayACPSurface(gatewayACPSurfaceDeps{
		sessions:         composition.sessions,
		appName:          composition.appName,
		userID:           composition.userID,
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
func (s *Stack) authenticateModelProvider(ctx context.Context, req modelconfig.AuthenticateRequest) error {
	if s == nil {
		return fmt.Errorf("gatewayapp: stack is unavailable")
	}
	template, ok := modelconfig.LookupProvider(req.Provider)
	if ok && template.AuthFlow == modelconfig.AuthFlowCodexOAuth {
		if s.composition.codexAuth == nil {
			return fmt.Errorf("gatewayapp: codex authentication is unavailable")
		}
		return s.composition.codexAuth.EnsureAuthenticated(ctx, codexauth.LoginOptions{
			HTTPClient:      req.HTTPClient,
			OpenBrowser:     req.OpenBrowser,
			CallbackTimeout: req.CallbackTimeout,
		})
	}
	if ok && template.AuthFlow == modelconfig.AuthFlowGrokOAuth {
		if s.composition.grokAuth == nil {
			return fmt.Errorf("gatewayapp: grok authentication is unavailable")
		}
		return s.composition.grokAuth.EnsureAuthenticated(ctx, grokauth.LoginOptions{
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
	if s.composition == nil || s.composition.providerUsage == nil {
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
	return s.composition.providerUsage.Query(ctx, config.Provider)
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
	s.composition.mu.RLock()
	runtimeCfg := s.composition.runtime
	defaultWorkspace := s.composition.workspace.CWD
	s.composition.mu.RUnlock()
	if strings.TrimSpace(workspaceDir) == "" {
		workspaceDir = defaultWorkspace
	}
	return DiscoverSkillMetaRequest(skill.DiscoverRequest{
		Dirs:          stackSkillDiscoveryDirs(workspaceDir, runtimeCfg.SkillDirs),
		WorkspaceDir:  workspaceDir,
		PluginBundles: skill.ClonePluginBundles(runtimeCfg.PluginSkills),
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
	return s.runtime.SkillCatalog
}

func (s StatusService) Doctor(ctx context.Context, req DoctorRequest) (DoctorReport, error) {
	return s.composition.Doctor(ctx, req)
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

// PreflightSandbox is a Host bootstrap diagnostic used before presentation
// clients are available. Product lifecycle mutations belong to the typed,
// principal-bound AppServer Configuration capability.
func (s StatusService) PreflightSandbox(ctx context.Context, allowNonElevatedRepair bool) (SandboxStatus, error) {
	if s.preflightSandboxFn == nil {
		return SandboxStatus{}, fmt.Errorf("gatewayapp: Host sandbox preflight is unavailable")
	}
	return s.preflightSandboxFn(ctx, allowNonElevatedRepair)
}

func (s StatusService) SessionRuntimeState(ctx context.Context, ref session.SessionRef) (SessionRuntimeState, error) {
	return s.composition.SessionRuntimeState(ctx, ref)
}
