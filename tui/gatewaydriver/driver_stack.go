package gatewaydriver

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/OnslaughtSnail/caelis/gateway"
	sdkcompact "github.com/OnslaughtSnail/caelis/sdk/compact"
	sdkcontroller "github.com/OnslaughtSnail/caelis/sdk/controller"
	sdkproviders "github.com/OnslaughtSnail/caelis/sdk/model/providers"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdkstream "github.com/OnslaughtSnail/caelis/sdk/stream"
)

type GatewayService interface {
	Streams() sdkstream.Service
	BeginTurn(context.Context, gateway.BeginTurnRequest) (gateway.BeginTurnResult, error)
	Interrupt(context.Context, gateway.InterruptRequest) error
	ResumeSession(context.Context, gateway.ResumeSessionRequest) (sdksession.LoadedSession, error)
	ListSessions(context.Context, gateway.ListSessionsRequest) (sdksession.SessionList, error)
	ReplayEvents(context.Context, gateway.ReplayEventsRequest) (gateway.ReplayEventsResult, error)
	ControlPlaneState(context.Context, gateway.ControlPlaneStateRequest) (gateway.ControlPlaneState, error)
	HandoffController(context.Context, gateway.HandoffControllerRequest) (sdksession.Session, error)
	AttachParticipant(context.Context, gateway.AttachParticipantRequest) (sdksession.Session, error)
	PromptParticipant(context.Context, gateway.PromptParticipantRequest) (gateway.BeginTurnResult, error)
	DetachParticipant(context.Context, gateway.DetachParticipantRequest) (sdksession.Session, error)
}

type ModelConfig struct {
	Alias                  string
	Provider               string
	API                    sdkproviders.APIType
	Model                  string
	BaseURL                string
	HTTPClient             *http.Client
	Token                  string
	TokenEnv               string
	PersistToken           bool
	AuthType               sdkproviders.AuthType
	HeaderKey              string
	ContextWindowTokens    int
	ReasoningEffort        string
	DefaultReasoningEffort string
	ReasoningLevels        []string
	ReasoningMode          string
	MaxOutputTok           int
	Timeout                time.Duration
}

type SessionRuntimeState struct {
	ModelAlias      string
	ReasoningEffort string
	SessionMode     string
	SandboxMode     string
}

type SandboxStatus struct {
	RequestedBackend string
	ResolvedBackend  string
	Route            string
	FallbackReason   string
	SecuritySummary  string
}

type DoctorRequest struct {
	SessionRef sdksession.SessionRef
	SessionID  string
	BindingKey string
}

type DoctorReport struct {
	StoreDir                string
	SessionID               string
	SessionMode             string
	ActiveModelAlias        string
	ActiveProvider          string
	ActiveModel             string
	MissingAPIKey           bool
	SandboxRequestedBackend string
	SandboxResolvedBackend  string
	SandboxRoute            string
	SandboxFallbackReason   string
	SandboxSecuritySummary  string
	HostExecution           bool
	FullAccessMode          bool
	ConfigPermissionsSecure bool
	Warnings                []string
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

type DriverStack struct {
	Gateway   GatewayService
	Sessions  sdksession.Service
	AppName   string
	UserID    string
	Workspace sdksession.WorkspaceRef

	StartSessionFn                       func(context.Context, string, string) (sdksession.Session, error)
	ACPControllerStatusFn                func(context.Context, sdksession.SessionRef) (sdkcontroller.ControllerStatus, bool, error)
	DefaultModelAliasFn                  func() string
	SandboxStatusFn                      func() SandboxStatus
	SessionRuntimeStateFn                func(context.Context, sdksession.SessionRef) (SessionRuntimeState, error)
	DoctorFn                             func(context.Context, DoctorRequest) (DoctorReport, error)
	ModelConfigFn                        func(string) (ModelConfig, bool)
	SessionUsageSnapshotFn               func(context.Context, sdksession.SessionRef, string) (sdkcompact.UsageSnapshot, error)
	CompactSessionFn                     func(context.Context, sdksession.SessionRef) error
	ConnectFn                            func(ModelConfig) (string, error)
	UseModelFn                           func(context.Context, sdksession.SessionRef, string, ...string) error
	DeleteModelFn                        func(context.Context, sdksession.SessionRef, string) error
	SetACPControllerModelFn              func(context.Context, sdksession.SessionRef, string, string) (sdkcontroller.ControllerStatus, error)
	CycleSessionModeFn                   func(context.Context, sdksession.SessionRef) (string, error)
	SetSandboxBackendFn                  func(context.Context, string) (SandboxStatus, error)
	SetACPControllerModeFn               func(context.Context, sdksession.SessionRef, string) (sdkcontroller.ControllerStatus, error)
	SetSessionModeFn                     func(context.Context, sdksession.SessionRef, string) (string, error)
	RegisterBuiltinACPAgentWithOptionsFn func(context.Context, string, RegisterBuiltinACPAgentOptions) error
	UnregisterACPAgentFn                 func(string) error
	ListModelAliasesFn                   func(context.Context, sdksession.SessionRef) ([]string, error)
	ListProviderModelsFn                 func(string) []string
	ListBuiltinACPAgentAddOptionsFn      func() []ACPAgentAddOption
	ListInstallableACPAgentOptionsFn     func() []ACPAgentAddOption
	ListACPAgentsFn                      func() []ACPAgentInfo
}

func (s *DriverStack) StartSession(ctx context.Context, preferredSessionID string, bindingKey string) (sdksession.Session, error) {
	if s == nil || s.StartSessionFn == nil {
		return sdksession.Session{}, fmt.Errorf("tui/gatewaydriver: start session dependency is unavailable")
	}
	return s.StartSessionFn(ctx, preferredSessionID, bindingKey)
}

func (s *DriverStack) ACPControllerStatus(ctx context.Context, ref sdksession.SessionRef) (sdkcontroller.ControllerStatus, bool, error) {
	if s == nil || s.ACPControllerStatusFn == nil {
		return sdkcontroller.ControllerStatus{}, false, nil
	}
	return s.ACPControllerStatusFn(ctx, ref)
}

func (s *DriverStack) DefaultModelAlias() string {
	if s == nil || s.DefaultModelAliasFn == nil {
		return ""
	}
	return s.DefaultModelAliasFn()
}

func (s *DriverStack) SandboxStatus() SandboxStatus {
	if s == nil || s.SandboxStatusFn == nil {
		return SandboxStatus{}
	}
	return s.SandboxStatusFn()
}

func (s *DriverStack) SessionRuntimeState(ctx context.Context, ref sdksession.SessionRef) (SessionRuntimeState, error) {
	if s == nil || s.SessionRuntimeStateFn == nil {
		return SessionRuntimeState{}, fmt.Errorf("tui/gatewaydriver: session runtime state dependency is unavailable")
	}
	return s.SessionRuntimeStateFn(ctx, ref)
}

func (s *DriverStack) Doctor(ctx context.Context, req DoctorRequest) (DoctorReport, error) {
	if s == nil || s.DoctorFn == nil {
		return DoctorReport{}, fmt.Errorf("tui/gatewaydriver: doctor dependency is unavailable")
	}
	return s.DoctorFn(ctx, req)
}

func (s *DriverStack) ModelConfig(alias string) (ModelConfig, bool) {
	if s == nil || s.ModelConfigFn == nil {
		return ModelConfig{}, false
	}
	return s.ModelConfigFn(alias)
}

func (s *DriverStack) SessionUsageSnapshot(ctx context.Context, ref sdksession.SessionRef, modelText string) (sdkcompact.UsageSnapshot, error) {
	if s == nil || s.SessionUsageSnapshotFn == nil {
		return sdkcompact.UsageSnapshot{}, fmt.Errorf("tui/gatewaydriver: session usage dependency is unavailable")
	}
	return s.SessionUsageSnapshotFn(ctx, ref, modelText)
}

func (s *DriverStack) CompactSession(ctx context.Context, ref sdksession.SessionRef) error {
	if s == nil || s.CompactSessionFn == nil {
		return fmt.Errorf("tui/gatewaydriver: compact dependency is unavailable")
	}
	return s.CompactSessionFn(ctx, ref)
}

func (s *DriverStack) Connect(cfg ModelConfig) (string, error) {
	if s == nil || s.ConnectFn == nil {
		return "", fmt.Errorf("tui/gatewaydriver: connect dependency is unavailable")
	}
	return s.ConnectFn(cfg)
}

func (s *DriverStack) UseModel(ctx context.Context, ref sdksession.SessionRef, alias string, reasoning ...string) error {
	if s == nil || s.UseModelFn == nil {
		return fmt.Errorf("tui/gatewaydriver: use model dependency is unavailable")
	}
	return s.UseModelFn(ctx, ref, alias, reasoning...)
}

func (s *DriverStack) DeleteModel(ctx context.Context, ref sdksession.SessionRef, alias string) error {
	if s == nil || s.DeleteModelFn == nil {
		return fmt.Errorf("tui/gatewaydriver: delete model dependency is unavailable")
	}
	return s.DeleteModelFn(ctx, ref, alias)
}

func (s *DriverStack) SetACPControllerModel(ctx context.Context, ref sdksession.SessionRef, model string, reasoning string) (sdkcontroller.ControllerStatus, error) {
	if s == nil || s.SetACPControllerModelFn == nil {
		return sdkcontroller.ControllerStatus{}, fmt.Errorf("tui/gatewaydriver: ACP controller model dependency is unavailable")
	}
	return s.SetACPControllerModelFn(ctx, ref, model, reasoning)
}

func (s *DriverStack) CycleSessionMode(ctx context.Context, ref sdksession.SessionRef) (string, error) {
	if s == nil || s.CycleSessionModeFn == nil {
		return "", fmt.Errorf("tui/gatewaydriver: cycle mode dependency is unavailable")
	}
	return s.CycleSessionModeFn(ctx, ref)
}

func (s *DriverStack) SetSandboxBackend(ctx context.Context, backend string) (SandboxStatus, error) {
	if s == nil || s.SetSandboxBackendFn == nil {
		return SandboxStatus{}, fmt.Errorf("tui/gatewaydriver: sandbox backend dependency is unavailable")
	}
	return s.SetSandboxBackendFn(ctx, backend)
}

func (s *DriverStack) SetACPControllerMode(ctx context.Context, ref sdksession.SessionRef, mode string) (sdkcontroller.ControllerStatus, error) {
	if s == nil || s.SetACPControllerModeFn == nil {
		return sdkcontroller.ControllerStatus{}, fmt.Errorf("tui/gatewaydriver: ACP controller mode dependency is unavailable")
	}
	return s.SetACPControllerModeFn(ctx, ref, mode)
}

func (s *DriverStack) SetSessionMode(ctx context.Context, ref sdksession.SessionRef, mode string) (string, error) {
	if s == nil || s.SetSessionModeFn == nil {
		return "", fmt.Errorf("tui/gatewaydriver: session mode dependency is unavailable")
	}
	return s.SetSessionModeFn(ctx, ref, mode)
}

func (s *DriverStack) RegisterBuiltinACPAgentWithOptions(ctx context.Context, target string, opts RegisterBuiltinACPAgentOptions) error {
	if s == nil || s.RegisterBuiltinACPAgentWithOptionsFn == nil {
		return fmt.Errorf("tui/gatewaydriver: builtin ACP agent dependency is unavailable")
	}
	return s.RegisterBuiltinACPAgentWithOptionsFn(ctx, target, opts)
}

func (s *DriverStack) UnregisterACPAgent(target string) error {
	if s == nil || s.UnregisterACPAgentFn == nil {
		return fmt.Errorf("tui/gatewaydriver: ACP agent unregister dependency is unavailable")
	}
	return s.UnregisterACPAgentFn(target)
}

func (s *DriverStack) ListModelAliases(ctx context.Context, ref sdksession.SessionRef) ([]string, error) {
	if s == nil || s.ListModelAliasesFn == nil {
		return nil, fmt.Errorf("tui/gatewaydriver: model alias dependency is unavailable")
	}
	return s.ListModelAliasesFn(ctx, ref)
}

func (s *DriverStack) ListProviderModels(provider string) []string {
	if s == nil || s.ListProviderModelsFn == nil {
		return nil
	}
	return s.ListProviderModelsFn(provider)
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
