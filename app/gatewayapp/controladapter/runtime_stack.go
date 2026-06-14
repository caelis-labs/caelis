package controladapter

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/compact"
	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/skill"
	"github.com/OnslaughtSnail/caelis/ports/stream"
)

type GatewayService interface {
	Streams() stream.Service
	BeginTurn(context.Context, gateway.BeginTurnRequest) (gateway.BeginTurnResult, error)
	SubmitActiveTurn(context.Context, gateway.SubmitActiveTurnRequest) error
	Interrupt(context.Context, gateway.InterruptRequest) error
	ResumeSession(context.Context, gateway.ResumeSessionRequest) (session.LoadedSession, error)
	ListSessions(context.Context, gateway.ListSessionsRequest) (session.SessionList, error)
	ReplayEvents(context.Context, gateway.ReplayEventsRequest) (gateway.ReplayEventsResult, error)
	ControlPlaneState(context.Context, gateway.ControlPlaneStateRequest) (gateway.ControlPlaneState, error)
	HandoffController(context.Context, gateway.HandoffControllerRequest) (session.Session, error)
	AttachParticipant(context.Context, gateway.AttachParticipantRequest) (session.Session, error)
	PromptParticipant(context.Context, gateway.PromptParticipantRequest) (gateway.BeginTurnResult, error)
	DetachParticipant(context.Context, gateway.DetachParticipantRequest) (session.Session, error)
	ActiveTurns() []gateway.ActiveTurnState
}

type ModelConfig struct {
	ID                      string
	Alias                   string
	Provider                string
	ProfileID               string
	EndpointID              string
	API                     model.APIType
	Model                   string
	BaseURL                 string
	HTTPClient              *http.Client
	Token                   string
	TokenEnv                string
	PersistToken            bool
	AuthType                model.AuthType
	HeaderKey               string
	ContextWindowTokens     int
	ReasoningEffort         string
	DefaultReasoningEffort  string
	ReasoningLevels         []string
	ReasoningMode           string
	MaxOutputTok            int
	Timeout                 time.Duration
	StreamFirstEventTimeout time.Duration
}

type ModelCapabilityInfo struct {
	ContextWindowTokens    int
	DefaultMaxOutputTokens int
	MaxOutputTokens        int
	ReasoningEfforts       []string
	DefaultReasoningEffort string
	SupportsReasoning      bool
	SupportsToolCalls      bool
	SupportsImages         bool
	SupportsJSON           bool
}

type ModelChoice struct {
	ID         string
	Alias      string
	Provider   string
	Model      string
	ProfileID  string
	EndpointID string
	BaseURL    string
	Detail     string
}

type SessionRuntimeState struct {
	ModelID         string
	ModelAlias      string
	ReasoningEffort string
	SessionMode     string
	SandboxMode     string
}

type SandboxStatus struct {
	RequestedBackend         string
	ResolvedBackend          string
	Route                    string
	FallbackReason           string
	InstallHint              string
	Setup                    sandbox.SetupStatus
	SetupRequired            bool
	SetupError               string
	SetupMarkerCurrent       bool
	SetupMarkerReason        string
	SecuritySummary          string
	GlobalSetupCurrent       bool
	GlobalSetupRequired      bool
	GlobalSetupReason        string
	WorkspaceSetupCurrent    bool
	WorkspaceSetupRequired   bool
	WorkspaceSetupReason     string
	WorkspaceSetupRoot       string
	WorkspaceSetupWriteRoots int
	WorkspaceSetupPolicyHash string
	WorkspaceSetupUpdatedAt  time.Time
}

type DoctorRequest struct {
	SessionRef session.SessionRef
	SessionID  string
	BindingKey string
}

type DoctorReport struct {
	StoreDir                        string
	SessionID                       string
	SessionMode                     string
	ActiveModelAlias                string
	ActiveProvider                  string
	ActiveModel                     string
	MissingAPIKey                   bool
	SandboxRequestedBackend         string
	SandboxResolvedBackend          string
	SandboxRoute                    string
	SandboxFallbackReason           string
	SandboxInstallHint              string
	SandboxSetup                    *sandbox.SetupStatus
	SandboxSetupRequired            bool
	SandboxSetupError               string
	SandboxSetupMarkerCurrent       bool
	SandboxSetupMarkerReason        string
	SandboxSecuritySummary          string
	SandboxGlobalSetupCurrent       bool
	SandboxGlobalSetupRequired      bool
	SandboxGlobalSetupReason        string
	SandboxWorkspaceSetupCurrent    bool
	SandboxWorkspaceSetupRequired   bool
	SandboxWorkspaceSetupReason     string
	SandboxWorkspaceSetupRoot       string
	SandboxWorkspaceSetupWriteRoots int
	SandboxWorkspaceSetupPolicyHash string
	SandboxWorkspaceSetupUpdatedAt  time.Time
	HostExecution                   bool
	FullAccessMode                  bool
	ConfigPermissionsSecure         bool
	Warnings                        []string
}

type RegisterBuiltinACPAgentOptions struct {
	Install bool
}

type ACPAgentInfo struct {
	Name        string
	Description string
}

type ACPAgentAddOption struct {
	Value   string
	Display string
	Detail  string
}

type CodeFreeAuthRequest struct {
	BaseURL         string
	OpenBrowser     bool
	CallbackTimeout time.Duration
}

type RuntimeStack struct {
	GatewayFn func() GatewayService
	Sessions  session.Service
	AppName   string
	UserID    string
	Workspace session.WorkspaceRef

	StartSessionFn                       func(context.Context, string, string) (session.Session, error)
	ACPControllerStatusFn                func(context.Context, session.SessionRef) (controller.ControllerStatus, bool, error)
	DefaultModelAliasFn                  func() string
	SandboxStatusFn                      func() SandboxStatus
	SessionRuntimeStateFn                func(context.Context, session.SessionRef) (SessionRuntimeState, error)
	DoctorFn                             func(context.Context, DoctorRequest) (DoctorReport, error)
	ModelConfigFn                        func(string) (ModelConfig, bool)
	SessionUsageSnapshotFn               func(context.Context, session.SessionRef, string) (compact.UsageSnapshot, error)
	CompactSessionFn                     func(context.Context, session.SessionRef) error
	ConnectFn                            func(ModelConfig) (string, error)
	UseModelFn                           func(context.Context, session.SessionRef, string, ...string) error
	DeleteModelFn                        func(context.Context, session.SessionRef, string) error
	SetACPControllerModelFn              func(context.Context, session.SessionRef, string, string) (controller.ControllerStatus, error)
	CycleSessionModeFn                   func(context.Context, session.SessionRef) (string, error)
	SetSandboxBackendFn                  func(context.Context, string) (SandboxStatus, error)
	PrepareSandboxFn                     func(context.Context) (SandboxStatus, error)
	RepairSandboxFn                      func(context.Context) (SandboxStatus, error)
	PreflightSandboxFn                   func(context.Context, bool) (SandboxStatus, error)
	ResetSandboxFn                       func(context.Context) (SandboxStatus, error)
	SetACPControllerModeFn               func(context.Context, session.SessionRef, string) (controller.ControllerStatus, error)
	SetSessionModeFn                     func(context.Context, session.SessionRef, string) (string, error)
	RegisterBuiltinACPAgentWithOptionsFn func(context.Context, string, RegisterBuiltinACPAgentOptions) error
	RegisterACPAgentFn                   func(context.Context, CustomAgentConfig) error
	UnregisterACPAgentFn                 func(string) error
	ListModelAliasesFn                   func(context.Context, session.SessionRef) ([]string, error)
	ListModelChoicesFn                   func(context.Context, session.SessionRef) ([]ModelChoice, error)
	ListProviderModelsFn                 func(string) []string
	ListCatalogModelsFn                  func(string) []string
	DefaultModelCapabilitiesFn           func() ModelCapabilityInfo
	LookupModelCapabilitiesFn            func(string, string) (ModelCapabilityInfo, bool)
	ReasoningLevelsForModelFn            func(string, string) []string
	EnsureCodeFreeAuthFn                 func(context.Context, CodeFreeAuthRequest) error
	EnsureCodeFreeModelSelectionAuthFn   func(context.Context, CodeFreeAuthRequest) error
	DiscoverSkillsFn                     func(context.Context, string) ([]skill.Meta, error)
	ListBuiltinACPAgentAddOptionsFn      func() []ACPAgentAddOption
	ListInstallableACPAgentOptionsFn     func() []ACPAgentAddOption
	ListACPAgentsFn                      func() []ACPAgentInfo
	AgentProfileStatusFn                 func(context.Context) (AgentProfileStatusSnapshot, error)
	BindAgentProfileFn                   func(context.Context, AgentProfileBindingConfig) (AgentProfileStatusSnapshot, error)
	ListPluginsFn                        func(context.Context) ([]PluginSnapshot, error)
	AddMarketplaceFn                     func(context.Context, string) (MarketplaceSnapshot, error)
	ListMarketplacesFn                   func(context.Context) ([]MarketplaceSnapshot, error)
	UpdateMarketplaceFn                  func(context.Context, string) (MarketplaceSnapshot, error)
	RemoveMarketplaceFn                  func(context.Context, string) error
	DiscoverOpenCodeFn                   func(context.Context, string) (OpenCodeDiscoverySnapshot, error)
	ImportOpenCodeFn                     func(context.Context, string) ([]PluginSnapshot, error)
	AddPluginPathFn                      func(context.Context, string) (PluginSnapshot, error)
	InstallPluginFn                      func(context.Context, string) (PluginSnapshot, error)
	EnablePluginFn                       func(context.Context, string) (PluginSnapshot, error)
	DisablePluginFn                      func(context.Context, string) (PluginSnapshot, error)
	RemovePluginFn                       func(context.Context, string) error
	InspectPluginFn                      func(context.Context, string) (PluginSnapshot, error)
}

func (s *RuntimeStack) gateway() (GatewayService, error) {
	if s == nil || s.GatewayFn == nil {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: gateway dependency is unavailable")
	}
	gw := s.GatewayFn()
	if gw == nil {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: gateway is unavailable")
	}
	return gw, nil
}

func (s *RuntimeStack) StartSession(ctx context.Context, preferredSessionID string, bindingKey string) (session.Session, error) {
	if s == nil || s.StartSessionFn == nil {
		return session.Session{}, fmt.Errorf("app/gatewayapp/controladapter: start session dependency is unavailable")
	}
	return s.StartSessionFn(ctx, preferredSessionID, bindingKey)
}

func (s *RuntimeStack) ACPControllerStatus(ctx context.Context, ref session.SessionRef) (controller.ControllerStatus, bool, error) {
	if s == nil || s.ACPControllerStatusFn == nil {
		return controller.ControllerStatus{}, false, nil
	}
	return s.ACPControllerStatusFn(ctx, ref)
}

func (s *RuntimeStack) DefaultModelAlias() string {
	if s == nil || s.DefaultModelAliasFn == nil {
		return ""
	}
	return s.DefaultModelAliasFn()
}

func (s *RuntimeStack) SandboxStatus() SandboxStatus {
	if s == nil || s.SandboxStatusFn == nil {
		return SandboxStatus{}
	}
	return s.SandboxStatusFn()
}

func (s *RuntimeStack) SessionRuntimeState(ctx context.Context, ref session.SessionRef) (SessionRuntimeState, error) {
	if s == nil || s.SessionRuntimeStateFn == nil {
		return SessionRuntimeState{}, fmt.Errorf("app/gatewayapp/controladapter: session runtime state dependency is unavailable")
	}
	return s.SessionRuntimeStateFn(ctx, ref)
}

func (s *RuntimeStack) Doctor(ctx context.Context, req DoctorRequest) (DoctorReport, error) {
	if s == nil || s.DoctorFn == nil {
		return DoctorReport{}, fmt.Errorf("app/gatewayapp/controladapter: doctor dependency is unavailable")
	}
	return s.DoctorFn(ctx, req)
}

func (s *RuntimeStack) ModelConfig(alias string) (ModelConfig, bool) {
	if s == nil || s.ModelConfigFn == nil {
		return ModelConfig{}, false
	}
	return s.ModelConfigFn(alias)
}

func (s *RuntimeStack) SessionUsageSnapshot(ctx context.Context, ref session.SessionRef, modelText string) (compact.UsageSnapshot, error) {
	if s == nil || s.SessionUsageSnapshotFn == nil {
		return compact.UsageSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: session usage dependency is unavailable")
	}
	return s.SessionUsageSnapshotFn(ctx, ref, modelText)
}

func (s *RuntimeStack) CompactSession(ctx context.Context, ref session.SessionRef) error {
	if s == nil || s.CompactSessionFn == nil {
		return fmt.Errorf("app/gatewayapp/controladapter: compact dependency is unavailable")
	}
	return s.CompactSessionFn(ctx, ref)
}

func (s *RuntimeStack) Connect(cfg ModelConfig) (string, error) {
	if s == nil || s.ConnectFn == nil {
		return "", fmt.Errorf("app/gatewayapp/controladapter: connect dependency is unavailable")
	}
	return s.ConnectFn(cfg)
}

func (s *RuntimeStack) UseModel(ctx context.Context, ref session.SessionRef, alias string, reasoning ...string) error {
	if s == nil || s.UseModelFn == nil {
		return fmt.Errorf("app/gatewayapp/controladapter: use model dependency is unavailable")
	}
	return s.UseModelFn(ctx, ref, alias, reasoning...)
}

func (s *RuntimeStack) DeleteModel(ctx context.Context, ref session.SessionRef, alias string) error {
	if s == nil || s.DeleteModelFn == nil {
		return fmt.Errorf("app/gatewayapp/controladapter: delete model dependency is unavailable")
	}
	return s.DeleteModelFn(ctx, ref, alias)
}

func (s *RuntimeStack) SetACPControllerModel(ctx context.Context, ref session.SessionRef, model string, reasoning string) (controller.ControllerStatus, error) {
	if s == nil || s.SetACPControllerModelFn == nil {
		return controller.ControllerStatus{}, fmt.Errorf("app/gatewayapp/controladapter: ACP controller model dependency is unavailable")
	}
	return s.SetACPControllerModelFn(ctx, ref, model, reasoning)
}

func (s *RuntimeStack) CycleSessionMode(ctx context.Context, ref session.SessionRef) (string, error) {
	if s == nil || s.CycleSessionModeFn == nil {
		return "", fmt.Errorf("app/gatewayapp/controladapter: cycle mode dependency is unavailable")
	}
	return s.CycleSessionModeFn(ctx, ref)
}

func (s *RuntimeStack) SetSandboxBackend(ctx context.Context, backend string) (SandboxStatus, error) {
	if s == nil || s.SetSandboxBackendFn == nil {
		return SandboxStatus{}, fmt.Errorf("app/gatewayapp/controladapter: sandbox backend dependency is unavailable")
	}
	return s.SetSandboxBackendFn(ctx, backend)
}

func (s *RuntimeStack) PrepareSandbox(ctx context.Context) (SandboxStatus, error) {
	if s == nil || s.PrepareSandboxFn == nil {
		return SandboxStatus{}, fmt.Errorf("app/gatewayapp/controladapter: sandbox repair dependency is unavailable")
	}
	return s.PrepareSandboxFn(ctx)
}

func (s *RuntimeStack) RepairSandbox(ctx context.Context) (SandboxStatus, error) {
	if s == nil || s.RepairSandboxFn == nil {
		return SandboxStatus{}, fmt.Errorf("app/gatewayapp/controladapter: sandbox repair dependency is unavailable")
	}
	return s.RepairSandboxFn(ctx)
}

func (s *RuntimeStack) PreflightSandbox(ctx context.Context, allowNonElevatedRepair bool) (SandboxStatus, error) {
	if s == nil || s.PreflightSandboxFn == nil {
		return s.SandboxStatus(), nil
	}
	return s.PreflightSandboxFn(ctx, allowNonElevatedRepair)
}

func (s *RuntimeStack) ResetSandbox(ctx context.Context) (SandboxStatus, error) {
	if s == nil || s.ResetSandboxFn == nil {
		return SandboxStatus{}, fmt.Errorf("app/gatewayapp/controladapter: sandbox reset dependency is unavailable")
	}
	return s.ResetSandboxFn(ctx)
}

func (s *RuntimeStack) SetACPControllerMode(ctx context.Context, ref session.SessionRef, mode string) (controller.ControllerStatus, error) {
	if s == nil || s.SetACPControllerModeFn == nil {
		return controller.ControllerStatus{}, fmt.Errorf("app/gatewayapp/controladapter: ACP controller mode dependency is unavailable")
	}
	return s.SetACPControllerModeFn(ctx, ref, mode)
}

func (s *RuntimeStack) SetSessionMode(ctx context.Context, ref session.SessionRef, mode string) (string, error) {
	if s == nil || s.SetSessionModeFn == nil {
		return "", fmt.Errorf("app/gatewayapp/controladapter: session mode dependency is unavailable")
	}
	return s.SetSessionModeFn(ctx, ref, mode)
}

func (s *RuntimeStack) RegisterBuiltinACPAgentWithOptions(ctx context.Context, target string, opts RegisterBuiltinACPAgentOptions) error {
	if s == nil || s.RegisterBuiltinACPAgentWithOptionsFn == nil {
		return fmt.Errorf("app/gatewayapp/controladapter: builtin ACP agent dependency is unavailable")
	}
	return s.RegisterBuiltinACPAgentWithOptionsFn(ctx, target, opts)
}

func (s *RuntimeStack) RegisterACPAgent(ctx context.Context, cfg CustomAgentConfig) error {
	if s == nil || s.RegisterACPAgentFn == nil {
		return fmt.Errorf("app/gatewayapp/controladapter: custom ACP agent dependency is unavailable")
	}
	return s.RegisterACPAgentFn(ctx, cfg)
}

func (s *RuntimeStack) UnregisterACPAgent(target string) error {
	if s == nil || s.UnregisterACPAgentFn == nil {
		return fmt.Errorf("app/gatewayapp/controladapter: ACP agent unregister dependency is unavailable")
	}
	return s.UnregisterACPAgentFn(target)
}

func (s *RuntimeStack) ListModelAliases(ctx context.Context, ref session.SessionRef) ([]string, error) {
	if s == nil || s.ListModelAliasesFn == nil {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: model alias dependency is unavailable")
	}
	return s.ListModelAliasesFn(ctx, ref)
}

func (s *RuntimeStack) ListModelChoices(ctx context.Context, ref session.SessionRef) ([]ModelChoice, error) {
	if s == nil || s.ListModelChoicesFn == nil {
		aliases, err := s.ListModelAliases(ctx, ref)
		if err != nil {
			return nil, err
		}
		choices := make([]ModelChoice, 0, len(aliases))
		for _, alias := range aliases {
			choices = append(choices, ModelChoice{ID: alias, Alias: alias})
		}
		return choices, nil
	}
	return s.ListModelChoicesFn(ctx, ref)
}

func (s *RuntimeStack) ListProviderModels(provider string) []string {
	if s == nil || s.ListProviderModelsFn == nil {
		return nil
	}
	return s.ListProviderModelsFn(provider)
}

func (s *RuntimeStack) ListCatalogModels(provider string) []string {
	if s == nil || s.ListCatalogModelsFn == nil {
		return nil
	}
	return s.ListCatalogModelsFn(provider)
}

func (s *RuntimeStack) DefaultModelCapabilities() ModelCapabilityInfo {
	if s == nil || s.DefaultModelCapabilitiesFn == nil {
		return ModelCapabilityInfo{
			ContextWindowTokens:    128000,
			DefaultMaxOutputTokens: 4096,
			MaxOutputTokens:        4096,
		}
	}
	return s.DefaultModelCapabilitiesFn()
}

func (s *RuntimeStack) LookupModelCapabilities(provider string, modelName string) (ModelCapabilityInfo, bool) {
	if s == nil || s.LookupModelCapabilitiesFn == nil {
		return ModelCapabilityInfo{}, false
	}
	return s.LookupModelCapabilitiesFn(provider, modelName)
}

func (s *RuntimeStack) ReasoningLevelsForModel(provider string, modelName string) []string {
	if s == nil || s.ReasoningLevelsForModelFn == nil {
		return nil
	}
	return s.ReasoningLevelsForModelFn(provider, modelName)
}

func (s *RuntimeStack) EnsureCodeFreeAuth(ctx context.Context, req CodeFreeAuthRequest) error {
	if s == nil || s.EnsureCodeFreeAuthFn == nil {
		return fmt.Errorf("app/gatewayapp/controladapter: codefree auth dependency is unavailable")
	}
	return s.EnsureCodeFreeAuthFn(ctx, req)
}

func (s *RuntimeStack) EnsureCodeFreeModelSelectionAuth(ctx context.Context, req CodeFreeAuthRequest) error {
	if s == nil || s.EnsureCodeFreeModelSelectionAuthFn == nil {
		return fmt.Errorf("app/gatewayapp/controladapter: codefree model auth dependency is unavailable")
	}
	return s.EnsureCodeFreeModelSelectionAuthFn(ctx, req)
}

func (s *RuntimeStack) DiscoverSkills(ctx context.Context, workspaceDir string) ([]skill.Meta, error) {
	if s == nil || s.DiscoverSkillsFn == nil {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: skill discovery dependency is unavailable")
	}
	return s.DiscoverSkillsFn(ctx, workspaceDir)
}

func (s *RuntimeStack) ListBuiltinACPAgentAddOptions() []ACPAgentAddOption {
	if s == nil || s.ListBuiltinACPAgentAddOptionsFn == nil {
		return nil
	}
	return s.ListBuiltinACPAgentAddOptionsFn()
}

func (s *RuntimeStack) ListInstallableACPAgentOptions() []ACPAgentAddOption {
	if s == nil || s.ListInstallableACPAgentOptionsFn == nil {
		return nil
	}
	return s.ListInstallableACPAgentOptionsFn()
}

func (s *RuntimeStack) ListACPAgents() []ACPAgentInfo {
	if s == nil || s.ListACPAgentsFn == nil {
		return nil
	}
	return s.ListACPAgentsFn()
}

func (s *RuntimeStack) AgentProfileStatus(ctx context.Context) (AgentProfileStatusSnapshot, error) {
	if s == nil || s.AgentProfileStatusFn == nil {
		return AgentProfileStatusSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: agent profile dependency is unavailable")
	}
	return s.AgentProfileStatusFn(ctx)
}

func (s *RuntimeStack) BindAgentProfile(ctx context.Context, cfg AgentProfileBindingConfig) (AgentProfileStatusSnapshot, error) {
	if s == nil || s.BindAgentProfileFn == nil {
		return AgentProfileStatusSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: agent profile binding dependency is unavailable")
	}
	return s.BindAgentProfileFn(ctx, cfg)
}

func (s *RuntimeStack) ListPlugins(ctx context.Context) ([]PluginSnapshot, error) {
	if s == nil || s.ListPluginsFn == nil {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: list plugins dependency is unavailable")
	}
	return s.ListPluginsFn(ctx)
}

func (s *RuntimeStack) AddMarketplace(ctx context.Context, source string) (MarketplaceSnapshot, error) {
	if s == nil || s.AddMarketplaceFn == nil {
		return MarketplaceSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: add marketplace dependency is unavailable")
	}
	return s.AddMarketplaceFn(ctx, source)
}

func (s *RuntimeStack) ListMarketplaces(ctx context.Context) ([]MarketplaceSnapshot, error) {
	if s == nil || s.ListMarketplacesFn == nil {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: list marketplaces dependency is unavailable")
	}
	return s.ListMarketplacesFn(ctx)
}

func (s *RuntimeStack) UpdateMarketplace(ctx context.Context, name string) (MarketplaceSnapshot, error) {
	if s == nil || s.UpdateMarketplaceFn == nil {
		return MarketplaceSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: update marketplace dependency is unavailable")
	}
	return s.UpdateMarketplaceFn(ctx, name)
}

func (s *RuntimeStack) RemoveMarketplace(ctx context.Context, name string) error {
	if s == nil || s.RemoveMarketplaceFn == nil {
		return fmt.Errorf("app/gatewayapp/controladapter: remove marketplace dependency is unavailable")
	}
	return s.RemoveMarketplaceFn(ctx, name)
}

func (s *RuntimeStack) DiscoverOpenCode(ctx context.Context, workspace string) (OpenCodeDiscoverySnapshot, error) {
	if s == nil || s.DiscoverOpenCodeFn == nil {
		return OpenCodeDiscoverySnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: discover opencode dependency is unavailable")
	}
	return s.DiscoverOpenCodeFn(ctx, workspace)
}

func (s *RuntimeStack) ImportOpenCode(ctx context.Context, workspace string) ([]PluginSnapshot, error) {
	if s == nil || s.ImportOpenCodeFn == nil {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: import opencode dependency is unavailable")
	}
	return s.ImportOpenCodeFn(ctx, workspace)
}

func (s *RuntimeStack) AddPluginPath(ctx context.Context, path string) (PluginSnapshot, error) {
	if s == nil || s.AddPluginPathFn == nil {
		return PluginSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: add plugin path dependency is unavailable")
	}
	return s.AddPluginPathFn(ctx, path)
}

func (s *RuntimeStack) InstallPlugin(ctx context.Context, source string) (PluginSnapshot, error) {
	if s == nil || s.InstallPluginFn == nil {
		return PluginSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: install plugin dependency is unavailable")
	}
	return s.InstallPluginFn(ctx, source)
}

func (s *RuntimeStack) EnablePlugin(ctx context.Context, id string) (PluginSnapshot, error) {
	if s == nil || s.EnablePluginFn == nil {
		return PluginSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: enable plugin dependency is unavailable")
	}
	return s.EnablePluginFn(ctx, id)
}

func (s *RuntimeStack) DisablePlugin(ctx context.Context, id string) (PluginSnapshot, error) {
	if s == nil || s.DisablePluginFn == nil {
		return PluginSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: disable plugin dependency is unavailable")
	}
	return s.DisablePluginFn(ctx, id)
}

func (s *RuntimeStack) RemovePlugin(ctx context.Context, id string) error {
	if s == nil || s.RemovePluginFn == nil {
		return fmt.Errorf("app/gatewayapp/controladapter: remove plugin dependency is unavailable")
	}
	return s.RemovePluginFn(ctx, id)
}

func (s *RuntimeStack) InspectPlugin(ctx context.Context, id string) (PluginSnapshot, error) {
	if s == nil || s.InspectPluginFn == nil {
		return PluginSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: inspect plugin dependency is unavailable")
	}
	return s.InspectPluginFn(ctx, id)
}
