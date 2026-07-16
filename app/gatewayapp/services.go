package gatewayapp

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	controlagents "github.com/caelis-labs/caelis/control/agents"
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

type ACPSurfaceService = gatewayACPSurface

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

func (s *Stack) ACPSurface(modes acp.ModeProvider, useFallbackModes bool, configs acp.ConfigProvider) ACPSurfaceService {
	return newGatewayACPSurface(s, modes, useFallbackModes, configs)
}

func (s ModelService) Connect(cfg ModelConfig) (string, error) {
	return s.stack.Connect(cfg)
}

// ConnectModels atomically adds one or more models sharing a provider profile.
func (s ModelService) ConnectModels(configs []ModelConfig) ([]string, error) {
	return s.stack.ConnectModels(configs)
}

func (s ModelService) Use(ctx context.Context, ref session.SessionRef, alias string, reasoningEffort ...string) error {
	return s.stack.UseModel(ctx, ref, alias, reasoningEffort...)
}

func (s ModelService) Delete(ctx context.Context, ref session.SessionRef, alias string) error {
	return s.stack.DeleteModel(ctx, ref, alias)
}

func (s ModelService) ListAliases(ctx context.Context, ref session.SessionRef) ([]string, error) {
	return s.stack.ListModelAliases(ctx, ref)
}

func (s ModelService) ListChoices(ctx context.Context, ref session.SessionRef) ([]ModelChoice, error) {
	return s.stack.ListModelChoices(ctx, ref)
}

func (s ModelService) DefaultAlias() string {
	return s.stack.DefaultModelAlias()
}

func (s ModelService) DefaultID() string {
	return s.stack.DefaultModelID()
}

func (s ModelService) Config(alias string) (ModelConfig, bool) {
	return s.stack.ModelConfig(alias)
}

func (s ModelService) HasAlias(alias string) bool {
	return s.stack.HasModelAlias(alias)
}

func (s ModelService) ListProviderModels(provider string) []string {
	return s.stack.ListProviderModels(provider)
}

func (s ModelService) UsageSnapshot(ctx context.Context, ref session.SessionRef, modelAlias string) (compact.UsageSnapshot, error) {
	return s.stack.SessionUsageSnapshot(ctx, ref, modelAlias)
}

func (s AgentService) ControllerStatus(ctx context.Context, ref session.SessionRef) (controller.ControllerStatus, bool, error) {
	return s.stack.ACPControllerStatus(ctx, ref)
}

func (s AgentService) SetControllerModel(ctx context.Context, ref session.SessionRef, model string, reasoningEffort string) (controller.ControllerStatus, error) {
	return s.stack.SetACPControllerModel(ctx, ref, model, reasoningEffort)
}

func (s AgentService) SetControllerMode(ctx context.Context, ref session.SessionRef, mode string) (controller.ControllerStatus, error) {
	return s.stack.SetACPControllerMode(ctx, ref, mode)
}

func (s AgentService) DiscoverConnection(ctx context.Context, req controlagents.ConnectRequest) (controlagents.DiscoverySnapshot, error) {
	return s.stack.DiscoverACPConnection(ctx, req)
}

func (s AgentService) Connect(ctx context.Context, req controlagents.ConnectRequest) (controlagents.ConnectResult, error) {
	return s.stack.ConnectACP(ctx, req)
}

func (s AgentService) DisconnectCandidates(ctx context.Context) ([]controlagents.DisconnectCandidate, error) {
	return s.stack.DisconnectCandidates(ctx)
}

func (s AgentService) Disconnect(ctx context.Context, agentID string) (controlagents.DisconnectResult, error) {
	return s.stack.DisconnectACP(ctx, agentID)
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

func (s StatusService) SetSandboxBackend(ctx context.Context, backend string) (SandboxStatus, error) {
	return s.stack.SetSandboxBackend(ctx, backend)
}

func (s StatusService) PrepareSandbox(ctx context.Context) (SandboxStatus, error) {
	return s.stack.PrepareSandbox(ctx)
}

func (s StatusService) RepairSandbox(ctx context.Context) (SandboxStatus, error) {
	return s.stack.RepairSandbox(ctx)
}

func (s StatusService) PreflightSandbox(ctx context.Context, allowNonElevatedRepair bool) (SandboxStatus, error) {
	return s.stack.PreflightSandbox(ctx, allowNonElevatedRepair)
}

func (s StatusService) ResetSandbox(ctx context.Context) (SandboxStatus, error) {
	return s.stack.ResetSandbox(ctx)
}

func (s StatusService) SessionRuntimeState(ctx context.Context, ref session.SessionRef) (SessionRuntimeState, error) {
	return s.stack.SessionRuntimeState(ctx, ref)
}

func (s StatusService) SetSessionMode(ctx context.Context, ref session.SessionRef, mode string) (string, error) {
	return s.stack.SetSessionMode(ctx, ref, mode)
}

func (s StatusService) CycleSessionMode(ctx context.Context, ref session.SessionRef) (string, error) {
	return s.stack.CycleSessionMode(ctx, ref)
}
