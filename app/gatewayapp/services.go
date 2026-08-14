package gatewayapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/codexauth"
	"github.com/caelis-labs/caelis/control/modelconfig/grokauth"
	"github.com/caelis-labs/caelis/control/modelconfig/providerusage"
	controller "github.com/caelis-labs/caelis/internal/acpagentbridge/controller"
	"github.com/caelis-labs/caelis/protocol/acp"
)

type ModelService struct {
	stack *Stack
}

type AgentService struct {
	stack *Stack
}

type SkillService struct {
	stack *Stack
}

type StatusService struct {
	stack *Stack
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

func (s *Stack) Models() ModelService {
	return ModelService{stack: s}
}

func (s *Stack) Agents() AgentService {
	return AgentService{stack: s}
}

func (s *Stack) Skills() SkillService {
	return SkillService{stack: s}
}

func (s *Stack) Status() StatusService {
	return StatusService{stack: s}
}

func (s *Stack) ACPSurface(modes acp.ModeProvider, useFallbackModes bool, configs acp.ConfigProvider) ACPPresentationService {
	return newGatewayACPSurface(s, modes, useFallbackModes, configs)
}

func (s ModelService) ListAliases(ctx context.Context, ref session.SessionRef) ([]string, error) {
	return s.stack.ListModelAliases(ctx, ref)
}

func (s ModelService) ListChoices(ctx context.Context, ref session.SessionRef) ([]ModelChoice, error) {
	return s.stack.ListModelChoices(ctx, ref)
}

func (s ModelService) HasReusableAuth(ctx context.Context, provider string, baseURL string) bool {
	return s.stack.HasReusableProviderAuth(ctx, provider, baseURL)
}

func (s ModelService) DefaultAlias() string {
	return s.stack.DefaultModelAlias()
}

func (s ModelService) DefaultEffort() string {
	return s.stack.DefaultModelEffort()
}

// EffectiveAlias returns the model selected for new work in this Host process.
// It may differ from the persisted Host default when startup flags provide a
// process-scoped ModelProfile override.
func (s ModelService) EffectiveAlias() string {
	return s.stack.EffectiveModelAlias()
}

// EffectiveEffort returns the reasoning effort selected for new work in this
// Host process. It follows the same startup-override semantics as
// EffectiveAlias.
func (s ModelService) EffectiveEffort() string {
	return s.stack.EffectiveModelEffort()
}

func (s ModelService) Config(alias string) (ModelConfig, bool) {
	return s.stack.ModelConfig(alias)
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
		if s.codexAuth == nil {
			return fmt.Errorf("gatewayapp: codex authentication is unavailable")
		}
		return s.codexAuth.EnsureAuthenticated(ctx, codexauth.LoginOptions{
			HTTPClient:      req.HTTPClient,
			OpenBrowser:     req.OpenBrowser,
			CallbackTimeout: req.CallbackTimeout,
		})
	}
	if ok && template.AuthFlow == modelconfig.AuthFlowGrokOAuth {
		if s.grokAuth == nil {
			return fmt.Errorf("gatewayapp: grok authentication is unavailable")
		}
		return s.grokAuth.EnsureAuthenticated(ctx, grokauth.LoginOptions{
			HTTPClient:      req.HTTPClient,
			OpenBrowser:     req.OpenBrowser,
			CallbackTimeout: req.CallbackTimeout,
		})
	}
	return modelconfig.AuthenticateProvider(ctx, req)
}

func (s ModelService) UsageSnapshot(ctx context.Context, ref session.SessionRef, modelAlias string) (compact.UsageSnapshot, error) {
	return s.stack.SessionUsageSnapshot(ctx, ref, modelAlias)
}

// ProviderUsage returns the latest cached account-level subscription windows
// for the selected model's provider and schedules a bounded asynchronous
// refresh. It never waits for the provider account API. found=false means the
// provider has no usage adapter or the model is not backed by a subscription
// credential.
func (s ModelService) ProviderUsage(ctx context.Context, modelAlias string) (providerusage.Snapshot, bool, error) {
	if s.stack == nil || s.stack.providerUsage == nil {
		return providerusage.Snapshot{}, false, nil
	}
	config, ok := s.stack.ModelConfig(modelAlias)
	if !ok {
		return providerusage.Snapshot{}, false, nil
	}
	switch config.CredentialRef {
	case modelconfig.CodexOAuthCredentialRef, modelconfig.GrokOAuthCredentialRef:
	default:
		return providerusage.Snapshot{}, false, nil
	}
	return s.stack.providerUsage.Query(ctx, config.Provider)
}

func (s AgentService) ControllerStatus(ctx context.Context, ref session.SessionRef) (controller.ControllerStatus, bool, error) {
	return s.stack.ACPControllerStatus(ctx, ref)
}

func (s AgentService) DisconnectCandidates(ctx context.Context) ([]controlagents.DisconnectCandidate, error) {
	return s.stack.DisconnectCandidates(ctx)
}

func (s AgentService) List() []ACPAgentInfo {
	return s.stack.ListACPAgents()
}

func (s SkillService) Discover(ctx context.Context, workspaceDir string) ([]SkillMeta, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if s.stack == nil {
		return DiscoverSkillMeta(nil, workspaceDir)
	}
	s.stack.mu.RLock()
	runtimeCfg := s.stack.runtime
	defaultWorkspace := s.stack.Workspace.CWD
	s.stack.mu.RUnlock()
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
	if s.stack == nil {
		return skill.Catalog{}
	}
	return s.stack.skillCatalogSnapshot()
}

func (s *Stack) skillCatalogSnapshot() skill.Catalog {
	if s == nil {
		return skill.Catalog{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.runtime.SkillCatalog
}

func (s StatusService) Doctor(ctx context.Context, req DoctorRequest) (DoctorReport, error) {
	return s.stack.Doctor(ctx, req)
}

func (s StatusService) Sandbox() SandboxStatus {
	return s.stack.SandboxStatus()
}

// PreflightSandbox is a Host bootstrap diagnostic used before presentation
// clients are available. Product lifecycle mutations belong to the typed,
// principal-bound AppServer Configuration capability.
func (s StatusService) PreflightSandbox(ctx context.Context, allowNonElevatedRepair bool) (SandboxStatus, error) {
	return s.stack.PreflightSandbox(ctx, allowNonElevatedRepair)
}

func (s StatusService) SessionRuntimeState(ctx context.Context, ref session.SessionRef) (SessionRuntimeState, error) {
	return s.stack.SessionRuntimeState(ctx, ref)
}
