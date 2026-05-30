package gatewaydriver

import (
	"context"
	"fmt"
	"net/http"
	"time"

	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/compact"
	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/skill"
	"github.com/OnslaughtSnail/caelis/ports/stream"
)

type GatewayService interface {
	Streams() stream.Service
	BeginTurn(context.Context, kernel.BeginTurnRequest) (kernel.BeginTurnResult, error)
	SubmitActiveTurn(context.Context, kernel.SubmitActiveTurnRequest) error
	Interrupt(context.Context, kernel.InterruptRequest) error
	ResumeSession(context.Context, kernel.ResumeSessionRequest) (session.LoadedSession, error)
	ListSessions(context.Context, kernel.ListSessionsRequest) (session.SessionList, error)
	ReplayEvents(context.Context, kernel.ReplayEventsRequest) (kernel.ReplayEventsResult, error)
	ControlPlaneState(context.Context, kernel.ControlPlaneStateRequest) (kernel.ControlPlaneState, error)
	HandoffController(context.Context, kernel.HandoffControllerRequest) (session.Session, error)
	AttachParticipant(context.Context, kernel.AttachParticipantRequest) (session.Session, error)
	PromptParticipant(context.Context, kernel.PromptParticipantRequest) (kernel.BeginTurnResult, error)
	DetachParticipant(context.Context, kernel.DetachParticipantRequest) (session.Session, error)
	ActiveTurns() []kernel.ActiveTurnState
}

type ModelConfig struct {
	ID                     string
	Alias                  string
	Provider               string
	ProfileID              string
	EndpointID             string
	API                    model.APIType
	Model                  string
	BaseURL                string
	HTTPClient             *http.Client
	Token                  string
	TokenEnv               string
	PersistToken           bool
	AuthType               model.AuthType
	HeaderKey              string
	ContextWindowTokens    int
	ReasoningEffort        string
	DefaultReasoningEffort string
	ReasoningLevels        []string
	ReasoningMode          string
	MaxOutputTok           int
	Timeout                time.Duration
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
	PermissionGrantCount            int
	PermissionReadRootCount         int
	PermissionWriteRootCount        int
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

type DriverStack struct {
	GatewayFn func() GatewayService
	Sessions  session.Service
	AppName   string
	UserID    string
	Workspace session.WorkspaceRef

	StartSessionFn                       func(context.Context, string, string) (session.Session, error)
	ACPControllerStatusFn                func(context.Context, session.SessionRef) (controller.ControllerStatus, bool, error)
	DefaultModelAliasFn                  func() string
	AppStatusViewFn                      func(context.Context, session.SessionRef) (appviewmodel.StatusView, error)
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
}

func (s *DriverStack) gateway() (GatewayService, error) {
	if s == nil || s.GatewayFn == nil {
		return nil, fmt.Errorf("surfaces/tui/gatewaydriver: gateway dependency is unavailable")
	}
	gw := s.GatewayFn()
	if gw == nil {
		return nil, fmt.Errorf("surfaces/tui/gatewaydriver: gateway is unavailable")
	}
	return gw, nil
}

func (s *DriverStack) StartSession(ctx context.Context, preferredSessionID string, bindingKey string) (session.Session, error) {
	if s == nil || s.StartSessionFn == nil {
		return session.Session{}, fmt.Errorf("surfaces/tui/gatewaydriver: start session dependency is unavailable")
	}
	return s.StartSessionFn(ctx, preferredSessionID, bindingKey)
}

func (s *DriverStack) ACPControllerStatus(ctx context.Context, ref session.SessionRef) (controller.ControllerStatus, bool, error) {
	if s == nil || s.ACPControllerStatusFn == nil {
		return controller.ControllerStatus{}, false, nil
	}
	return s.ACPControllerStatusFn(ctx, ref)
}

func (s *DriverStack) DefaultModelAlias() string {
	if s == nil || s.DefaultModelAliasFn == nil {
		return ""
	}
	return s.DefaultModelAliasFn()
}

func (s *DriverStack) AppStatusView(ctx context.Context, ref session.SessionRef) (appviewmodel.StatusView, bool, error) {
	if s == nil || s.AppStatusViewFn == nil {
		return appviewmodel.StatusView{}, false, nil
	}
	view, err := s.AppStatusViewFn(ctx, ref)
	return view, true, err
}

func (s *DriverStack) SandboxStatus() SandboxStatus {
	if s == nil || s.SandboxStatusFn == nil {
		return SandboxStatus{}
	}
	return s.SandboxStatusFn()
}

func (s *DriverStack) SessionRuntimeState(ctx context.Context, ref session.SessionRef) (SessionRuntimeState, error) {
	if s == nil || s.SessionRuntimeStateFn == nil {
		return SessionRuntimeState{}, fmt.Errorf("surfaces/tui/gatewaydriver: session runtime state dependency is unavailable")
	}
	return s.SessionRuntimeStateFn(ctx, ref)
}

func (s *DriverStack) Doctor(ctx context.Context, req DoctorRequest) (DoctorReport, error) {
	if s == nil || s.DoctorFn == nil {
		return DoctorReport{}, fmt.Errorf("surfaces/tui/gatewaydriver: doctor dependency is unavailable")
	}
	return s.DoctorFn(ctx, req)
}

func (s *DriverStack) ModelConfig(alias string) (ModelConfig, bool) {
	if s == nil || s.ModelConfigFn == nil {
		return ModelConfig{}, false
	}
	return s.ModelConfigFn(alias)
}

func (s *DriverStack) SessionUsageSnapshot(ctx context.Context, ref session.SessionRef, modelText string) (compact.UsageSnapshot, error) {
	if s == nil || s.SessionUsageSnapshotFn == nil {
		return compact.UsageSnapshot{}, fmt.Errorf("surfaces/tui/gatewaydriver: session usage dependency is unavailable")
	}
	return s.SessionUsageSnapshotFn(ctx, ref, modelText)
}

func (s *DriverStack) CompactSession(ctx context.Context, ref session.SessionRef) error {
	if s == nil || s.CompactSessionFn == nil {
		return fmt.Errorf("surfaces/tui/gatewaydriver: compact dependency is unavailable")
	}
	return s.CompactSessionFn(ctx, ref)
}

func (s *DriverStack) Connect(cfg ModelConfig) (string, error) {
	if s == nil || s.ConnectFn == nil {
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: connect dependency is unavailable")
	}
	return s.ConnectFn(cfg)
}

func (s *DriverStack) UseModel(ctx context.Context, ref session.SessionRef, alias string, reasoning ...string) error {
	if s == nil || s.UseModelFn == nil {
		return fmt.Errorf("surfaces/tui/gatewaydriver: use model dependency is unavailable")
	}
	return s.UseModelFn(ctx, ref, alias, reasoning...)
}

func (s *DriverStack) DeleteModel(ctx context.Context, ref session.SessionRef, alias string) error {
	if s == nil || s.DeleteModelFn == nil {
		return fmt.Errorf("surfaces/tui/gatewaydriver: delete model dependency is unavailable")
	}
	return s.DeleteModelFn(ctx, ref, alias)
}

func (s *DriverStack) SetACPControllerModel(ctx context.Context, ref session.SessionRef, model string, reasoning string) (controller.ControllerStatus, error) {
	if s == nil || s.SetACPControllerModelFn == nil {
		return controller.ControllerStatus{}, fmt.Errorf("surfaces/tui/gatewaydriver: ACP controller model dependency is unavailable")
	}
	return s.SetACPControllerModelFn(ctx, ref, model, reasoning)
}

func (s *DriverStack) CycleSessionMode(ctx context.Context, ref session.SessionRef) (string, error) {
	if s == nil || s.CycleSessionModeFn == nil {
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: cycle mode dependency is unavailable")
	}
	return s.CycleSessionModeFn(ctx, ref)
}

func (s *DriverStack) SetSandboxBackend(ctx context.Context, backend string) (SandboxStatus, error) {
	if s == nil || s.SetSandboxBackendFn == nil {
		return SandboxStatus{}, fmt.Errorf("surfaces/tui/gatewaydriver: sandbox backend dependency is unavailable")
	}
	return s.SetSandboxBackendFn(ctx, backend)
}

func (s *DriverStack) PrepareSandbox(ctx context.Context) (SandboxStatus, error) {
	if s == nil || s.PrepareSandboxFn == nil {
		return SandboxStatus{}, fmt.Errorf("surfaces/tui/gatewaydriver: sandbox repair dependency is unavailable")
	}
	return s.PrepareSandboxFn(ctx)
}

func (s *DriverStack) RepairSandbox(ctx context.Context) (SandboxStatus, error) {
	if s == nil || s.RepairSandboxFn == nil {
		return SandboxStatus{}, fmt.Errorf("surfaces/tui/gatewaydriver: sandbox repair dependency is unavailable")
	}
	return s.RepairSandboxFn(ctx)
}

func (s *DriverStack) PreflightSandbox(ctx context.Context, allowNonElevatedRepair bool) (SandboxStatus, error) {
	if s == nil || s.PreflightSandboxFn == nil {
		return s.SandboxStatus(), nil
	}
	return s.PreflightSandboxFn(ctx, allowNonElevatedRepair)
}

func (s *DriverStack) ResetSandbox(ctx context.Context) (SandboxStatus, error) {
	if s == nil || s.ResetSandboxFn == nil {
		return SandboxStatus{}, fmt.Errorf("surfaces/tui/gatewaydriver: sandbox reset dependency is unavailable")
	}
	return s.ResetSandboxFn(ctx)
}

func (s *DriverStack) SetACPControllerMode(ctx context.Context, ref session.SessionRef, mode string) (controller.ControllerStatus, error) {
	if s == nil || s.SetACPControllerModeFn == nil {
		return controller.ControllerStatus{}, fmt.Errorf("surfaces/tui/gatewaydriver: ACP controller mode dependency is unavailable")
	}
	return s.SetACPControllerModeFn(ctx, ref, mode)
}

func (s *DriverStack) SetSessionMode(ctx context.Context, ref session.SessionRef, mode string) (string, error) {
	if s == nil || s.SetSessionModeFn == nil {
		return "", fmt.Errorf("surfaces/tui/gatewaydriver: session mode dependency is unavailable")
	}
	return s.SetSessionModeFn(ctx, ref, mode)
}

func (s *DriverStack) RegisterBuiltinACPAgentWithOptions(ctx context.Context, target string, opts RegisterBuiltinACPAgentOptions) error {
	if s == nil || s.RegisterBuiltinACPAgentWithOptionsFn == nil {
		return fmt.Errorf("surfaces/tui/gatewaydriver: builtin ACP agent dependency is unavailable")
	}
	return s.RegisterBuiltinACPAgentWithOptionsFn(ctx, target, opts)
}

func (s *DriverStack) RegisterACPAgent(ctx context.Context, cfg CustomAgentConfig) error {
	if s == nil || s.RegisterACPAgentFn == nil {
		return fmt.Errorf("surfaces/tui/gatewaydriver: custom ACP agent dependency is unavailable")
	}
	return s.RegisterACPAgentFn(ctx, cfg)
}

func (s *DriverStack) UnregisterACPAgent(target string) error {
	if s == nil || s.UnregisterACPAgentFn == nil {
		return fmt.Errorf("surfaces/tui/gatewaydriver: ACP agent unregister dependency is unavailable")
	}
	return s.UnregisterACPAgentFn(target)
}

func (s *DriverStack) ListModelAliases(ctx context.Context, ref session.SessionRef) ([]string, error) {
	if s == nil || s.ListModelAliasesFn == nil {
		return nil, fmt.Errorf("surfaces/tui/gatewaydriver: model alias dependency is unavailable")
	}
	return s.ListModelAliasesFn(ctx, ref)
}

func (s *DriverStack) ListModelChoices(ctx context.Context, ref session.SessionRef) ([]ModelChoice, error) {
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

func (s *DriverStack) ListProviderModels(provider string) []string {
	if s == nil || s.ListProviderModelsFn == nil {
		return nil
	}
	return s.ListProviderModelsFn(provider)
}

func (s *DriverStack) ListCatalogModels(provider string) []string {
	if s == nil || s.ListCatalogModelsFn == nil {
		return nil
	}
	return s.ListCatalogModelsFn(provider)
}

func (s *DriverStack) DefaultModelCapabilities() ModelCapabilityInfo {
	if s == nil || s.DefaultModelCapabilitiesFn == nil {
		return ModelCapabilityInfo{
			ContextWindowTokens:    128000,
			DefaultMaxOutputTokens: 4096,
			MaxOutputTokens:        4096,
		}
	}
	return s.DefaultModelCapabilitiesFn()
}

func (s *DriverStack) LookupModelCapabilities(provider string, modelName string) (ModelCapabilityInfo, bool) {
	if s == nil || s.LookupModelCapabilitiesFn == nil {
		return ModelCapabilityInfo{}, false
	}
	return s.LookupModelCapabilitiesFn(provider, modelName)
}

func (s *DriverStack) ReasoningLevelsForModel(provider string, modelName string) []string {
	if s == nil || s.ReasoningLevelsForModelFn == nil {
		return nil
	}
	return s.ReasoningLevelsForModelFn(provider, modelName)
}

func (s *DriverStack) EnsureCodeFreeAuth(ctx context.Context, req CodeFreeAuthRequest) error {
	if s == nil || s.EnsureCodeFreeAuthFn == nil {
		return fmt.Errorf("surfaces/tui/gatewaydriver: codefree auth dependency is unavailable")
	}
	return s.EnsureCodeFreeAuthFn(ctx, req)
}

func (s *DriverStack) EnsureCodeFreeModelSelectionAuth(ctx context.Context, req CodeFreeAuthRequest) error {
	if s == nil || s.EnsureCodeFreeModelSelectionAuthFn == nil {
		return fmt.Errorf("surfaces/tui/gatewaydriver: codefree model auth dependency is unavailable")
	}
	return s.EnsureCodeFreeModelSelectionAuthFn(ctx, req)
}

func (s *DriverStack) DiscoverSkills(ctx context.Context, workspaceDir string) ([]skill.Meta, error) {
	if s == nil || s.DiscoverSkillsFn == nil {
		return nil, fmt.Errorf("surfaces/tui/gatewaydriver: skill discovery dependency is unavailable")
	}
	return s.DiscoverSkillsFn(ctx, workspaceDir)
}

func (s *DriverStack) ListBuiltinACPAgentAddOptions() []ACPAgentAddOption {
	if s == nil || s.ListBuiltinACPAgentAddOptionsFn == nil {
		return nil
	}
	return s.ListBuiltinACPAgentAddOptionsFn()
}

func (s *DriverStack) ListInstallableACPAgentOptions() []ACPAgentAddOption {
	if s == nil || s.ListInstallableACPAgentOptionsFn == nil {
		return nil
	}
	return s.ListInstallableACPAgentOptionsFn()
}

func (s *DriverStack) ListACPAgents() []ACPAgentInfo {
	if s == nil || s.ListACPAgentsFn == nil {
		return nil
	}
	return s.ListACPAgentsFn()
}
