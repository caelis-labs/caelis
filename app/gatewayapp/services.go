package gatewayapp

import (
	"context"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/compact"
	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp"
)

type ModelService struct {
	stack *Stack
}

type AgentService struct {
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

func (s *Stack) Status() StatusService {
	return StatusService{stack: s}
}

func (s *Stack) ACPSurface(modes acp.ModeProvider, useFallbackModes bool, configs acp.ConfigProvider) ACPSurfaceService {
	return newGatewayACPSurface(s, modes, useFallbackModes, configs)
}

func (s *Stack) Kernel() kernel.Service {
	if s == nil {
		return nil
	}
	gateway := s.CurrentGateway()
	if gateway == nil {
		return nil
	}
	return gateway
}

func (s ModelService) Connect(cfg ModelConfig) (string, error) {
	return s.stack.Connect(cfg)
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

func (s AgentService) RegisterBuiltinWithOptions(ctx context.Context, name string, opts RegisterBuiltinACPAgentOptions) error {
	return s.stack.RegisterBuiltinACPAgentWithOptions(ctx, name, opts)
}

func (s AgentService) RegisterCustom(ctx context.Context, cfg AgentConfig) error {
	return s.stack.RegisterACPAgent(ctx, cfg)
}

func (s AgentService) Unregister(name string) error {
	return s.stack.UnregisterACPAgent(name)
}

func (s AgentService) List() []ACPAgentInfo {
	return s.stack.ListACPAgents()
}

func (s AgentService) BuiltinAddOptions() []ACPAgentAddOption {
	return s.stack.ListBuiltinACPAgentAddOptions()
}

func (s AgentService) InstallableOptions() []ACPAgentAddOption {
	return s.stack.ListInstallableACPAgentOptions()
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

func (s StatusService) SessionRuntimeState(ctx context.Context, ref session.SessionRef) (SessionRuntimeState, error) {
	return s.stack.SessionRuntimeState(ctx, ref)
}

func (s StatusService) SetSessionMode(ctx context.Context, ref session.SessionRef, mode string) (string, error) {
	return s.stack.SetSessionMode(ctx, ref, mode)
}

func (s StatusService) CycleSessionMode(ctx context.Context, ref session.SessionRef) (string, error) {
	return s.stack.CycleSessionMode(ctx, ref)
}
