package gatewayapp

import (
	"context"
	"time"

	sdkplacement "github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	sdksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	controller "github.com/caelis-labs/caelis/internal/acpagentbridge/controller"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
)

// runtimeProjection returns the process root's Runtime composition without
// exposing it outside gatewayapp. Keeping explicit Stack forwarding methods
// preserves the public Host API, including its historical nil-receiver
// behavior, while detached Session Runtimes use runtimeComposition directly.
func (s *Stack) runtimeProjection() *runtimeComposition {
	if s == nil {
		return nil
	}
	return &s.runtimeComposition
}

func (s *Stack) KernelTurnState() KernelTurnReader {
	return s.runtimeProjection().KernelTurnState()
}

func (s *Stack) KernelSessionState() KernelSessionReader {
	return s.runtimeProjection().KernelSessionState()
}

func (s *Stack) KernelControlPlaneState() KernelControlPlaneReader {
	return s.runtimeProjection().KernelControlPlaneState()
}

func (s *Stack) KernelStreams() kernelimpl.StreamProvider {
	return s.runtimeProjection().KernelStreams()
}

func (s *Stack) ControlRuntimeView() *ControlRuntimeView {
	return s.runtimeProjection().ControlRuntimeView()
}

func (s *Stack) Models() ModelService {
	return s.runtimeProjection().Models()
}

func (s *Stack) Agents() AgentService {
	return s.runtimeProjection().Agents()
}

func (s *Stack) Skills() SkillService {
	return s.runtimeProjection().Skills()
}

func (s *Stack) Plugins() PluginService {
	return s.runtimeProjection().Plugins()
}

func (s *Stack) ListModelAliases(ctx context.Context, ref session.SessionRef) ([]string, error) {
	return s.runtimeProjection().ListModelAliases(ctx, ref)
}

func (s *Stack) ListModelChoices(ctx context.Context, ref session.SessionRef) ([]ModelChoice, error) {
	return s.runtimeProjection().ListModelChoices(ctx, ref)
}

func (s *Stack) HasReusableProviderAuth(ctx context.Context, provider string, baseURL string) bool {
	return s.runtimeProjection().HasReusableProviderAuth(ctx, provider, baseURL)
}

func (s *Stack) DefaultModelAlias() string {
	return s.runtimeProjection().DefaultModelAlias()
}

func (s *Stack) DefaultModelEffort() string {
	return s.runtimeProjection().DefaultModelEffort()
}

func (s *Stack) EffectiveModelAlias() string {
	return s.runtimeProjection().EffectiveModelAlias()
}

func (s *Stack) EffectiveModelEffort() string {
	return s.runtimeProjection().EffectiveModelEffort()
}

func (s *Stack) ModelConfig(alias string) (ModelConfig, bool) {
	return s.runtimeProjection().ModelConfig(alias)
}

func (s *Stack) Doctor(ctx context.Context, req DoctorRequest) (DoctorReport, error) {
	return s.runtimeProjection().Doctor(ctx, req)
}

func (s *Stack) DoctorForWorkspace(ctx context.Context, workspace session.WorkspaceRef, req DoctorRequest) (DoctorReport, error) {
	return s.runtimeProjection().DoctorForWorkspace(ctx, workspace, req)
}

func (s *Stack) ACPControllerStatus(ctx context.Context, ref session.SessionRef) (controller.ControllerStatus, bool, error) {
	return s.runtimeProjection().ACPControllerStatus(ctx, ref)
}

func (s *Stack) ListACPAgents() []ACPAgentInfo {
	return s.runtimeProjection().ListACPAgents()
}

func (s *Stack) DisconnectCandidates(ctx context.Context) ([]controlagents.DisconnectCandidate, error) {
	return s.runtimeProjection().DisconnectCandidates(ctx)
}

func (s *Stack) DisconnectCandidatesSnapshot(ctx context.Context) (appserver.DisconnectCandidatesSnapshot, error) {
	return s.runtimeProjection().DisconnectCandidatesSnapshot(ctx)
}

func (s *Stack) ConfigurationRevision(ctx context.Context) (uint64, error) {
	return s.runtimeProjection().ConfigurationRevision(ctx)
}

func (s *Stack) ResolveHandlePlacement(ctx context.Context, handle agentbinding.Handle) (sdkplacement.Placement, error) {
	return s.runtimeProjection().ResolveHandlePlacement(ctx, handle)
}

func (s *Stack) LoadHistory(ctx context.Context, req sdksubagent.HistoryRequest) (session.LoadedSession, error) {
	return s.runtimeProjection().LoadHistory(ctx, req)
}

func (s *Stack) SandboxStatus() SandboxStatus {
	return s.runtimeProjection().SandboxStatus()
}

func (s *Stack) SandboxStatusForWorkspace(workspace session.WorkspaceRef) SandboxStatus {
	return s.runtimeProjection().SandboxStatusForWorkspace(workspace)
}

func (s *Stack) SessionRuntimeState(ctx context.Context, ref session.SessionRef) (SessionRuntimeState, error) {
	return s.runtimeProjection().SessionRuntimeState(ctx, ref)
}

func (s *Stack) StartSubagent(
	ctx context.Context,
	ref session.SessionRef,
	agent string,
	prompt string,
	source string,
) (task.Snapshot, error) {
	return s.runtimeProjection().StartSubagent(ctx, ref, agent, prompt, source)
}

func (s *Stack) StartSubagentWithOptions(
	ctx context.Context,
	ref session.SessionRef,
	agent string,
	prompt string,
	source string,
	opts StartSubagentOptions,
) (task.Snapshot, error) {
	return s.runtimeProjection().StartSubagentWithOptions(ctx, ref, agent, prompt, source, opts)
}

func (s *Stack) WaitSubagentTask(
	ctx context.Context,
	ref session.SessionRef,
	taskID string,
	yieldDuration time.Duration,
) (task.Snapshot, error) {
	return s.runtimeProjection().WaitSubagentTask(ctx, ref, taskID, yieldDuration)
}

func (s *Stack) CompactSession(ctx context.Context, ref session.SessionRef) error {
	return s.runtimeProjection().CompactSession(ctx, ref)
}

func (s *Stack) SessionUsageSnapshot(ctx context.Context, ref session.SessionRef, modelAlias string) (compact.UsageSnapshot, error) {
	return s.runtimeProjection().SessionUsageSnapshot(ctx, ref, modelAlias)
}
