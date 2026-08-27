package acp

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	runtimeacp "github.com/caelis-labs/caelis/internal/acpagentbridge"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
	protocolacp "github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/surfaces/internal/promptview"
)

// ClientsConfig constructs product ACP from focused AppServer clients only.
// Surfaces never receive Runtime, Kernel, or Stack handles through this path.
type ClientsConfig struct {
	Clients      appserver.AppServerClients
	AppName      string
	UserID       string
	WorkspaceKey string
	WorkspaceCWD string
	// ManagedSessionHistoryToken is opaque Host composition input forwarded to
	// the ACP bridge. The Surface neither interprets it nor owns its policy.
	ManagedSessionHistoryToken string
	// SystemSessionClient optionally addresses product-owned child Sessions.
	// When nil, the principal-bound Session
	// client is used; Host exact-target Reconnect authorizes owners without
	// granting RoleSystemSessionRuntime to Surface tokens.
	SystemSessionClient appserver.SessionClient
}

// NewFromClients builds the product ACP surface from focused clients only.
func NewFromClients(cfg ClientsConfig) (*ProductAgent, error) {
	if err := cfg.Clients.Validate(); err != nil {
		return nil, err
	}
	clients := cfg.Clients
	systemSessionClient := cfg.SystemSessionClient
	if systemSessionClient == nil {
		systemSessionClient = clients.Sessions
	}
	agent, err := runtimeacp.NewGatewayAgent(runtimeacp.GatewayAgentConfig{
		SessionClient:              clients.Sessions,
		ConfigurationClient:        clients.Configuration,
		PresentationClient:         clients.Presentation,
		AppName:                    firstNonEmpty(cfg.AppName, "caelis"),
		UserID:                     firstNonEmpty(cfg.UserID, "local-user"),
		WorkspaceKey:               strings.TrimSpace(cfg.WorkspaceKey),
		WorkspaceCWD:               strings.TrimSpace(cfg.WorkspaceCWD),
		ManagedSessionHistoryToken: strings.TrimSpace(cfg.ManagedSessionHistoryToken),
		TaskStreamClient:           clients.Tasks,
		SlashResultFormatter:       promptview.FormatSlashResult,
		PromptRouterFactory: func(ctx context.Context, activeSession session.Session) (controlprompt.Router, error) {
			turnSessions := clients.Sessions
			if sessionvisibility.IsSystemManagedSession(activeSession) {
				turnSessions = systemSessionClient
			}
			driverWithTypedTurns, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
				SessionID:     strings.TrimSpace(activeSession.SessionID),
				WorkspaceKey:  strings.TrimSpace(activeSession.WorkspaceKey),
				WorkspaceDir:  strings.TrimSpace(activeSession.CWD),
				Surface:       "acp",
				Sessions:      turnSessions,
				Participants:  clients.Participants,
				Status:        clients.Status,
				Configuration: clients.Configuration,
				Agents:        clients.Agents,
				Completion:    clients.Completion,
				Plugins:       clients.Plugins,
			})
			if err != nil {
				return nil, err
			}
			router := controlprompt.New(controlprompt.RouterConfig{
				Service: driverWithTypedTurns,
				CommandNames: func(ctx context.Context, service controlprompt.RouterService) []string {
					handles, _ := clients.Participants.Handles(ctx, activeSession.SessionID)
					out := acpPromptCommandNamesFromHandles(handles)
					status, err := service.AgentStatus(ctx)
					if err != nil {
						return out
					}
					return controlagents.AppendRunNames(out, acpDirectAgentRuns(status), nil)
				},
				CoreCommandAllowed: func(_ context.Context, command string) bool {
					return controlprompt.IsACPKnown(command)
				},
			})
			return router, nil
		},
	})
	if err != nil {
		return nil, err
	}
	return &ProductAgent{inner: agent}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func acpPromptCommandNames(status agentbinding.Status) []string {
	return agentbinding.ProjectBoundDirectNames(controlprompt.DefaultACPNames(), status)
}

func acpPromptCommandNamesFromHandles(handles []string) []string {
	out := agentbinding.ProjectBoundDirectNames(controlprompt.DefaultACPNames(), agentbinding.Status{})
	seen := make(map[string]struct{}, len(out)+len(handles))
	for _, name := range out {
		seen[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	for _, raw := range handles {
		name := controlagents.NormalizeName(raw)
		if !controlagents.IsName(name) {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		out = append(out, name)
		seen[name] = struct{}{}
	}
	return out
}

func acpDirectAgentRuns(status controlprompt.AgentStatusSnapshot) []controlagents.Run {
	runs := make([]controlagents.Run, 0, len(status.Participants))
	for _, participant := range status.Participants {
		runs = append(runs, controlagents.DirectRunFromParticipant(participant.Label, participant.Kind, participant.Role, participant.Source))
	}
	return runs
}

// Agent is the product ACP surface consumed by ServeStdio. Its callback-aware
// prompt and load methods preserve Caelis's per-connection update and approval
// routing while the wire connection itself remains owned by acp-go-sdk.
type Agent interface {
	Initialize(context.Context, protocolacp.InitializeRequest) (protocolacp.InitializeResponse, error)
	NewSession(context.Context, protocolacp.NewSessionRequest) (protocolacp.NewSessionResponse, error)
	Prompt(context.Context, protocolacp.PromptRequest, PromptCallbacks) (protocolacp.PromptResponse, error)
	Cancel(context.Context, protocolacp.CancelNotification) error
}

// PromptCallbacks belongs to the connection Surface: updates and permission
// requests must return through the exact client connection that submitted the
// prompt.
type PromptCallbacks interface {
	SessionUpdate(context.Context, protocolacp.SessionNotification) error
	RequestPermission(context.Context, protocolacp.RequestPermissionRequest) (protocolacp.RequestPermissionResponse, error)
}

// ProductAgent is the Surface-owned connection contract around Caelis's
// private AppServer-to-ACP bridge. Callers do not receive the concrete bridge
// implementation through product composition.
type ProductAgent struct {
	inner *runtimeacp.RuntimeAgent
}

var (
	_ Agent               = (*ProductAgent)(nil)
	_ sessionLister       = (*ProductAgent)(nil)
	_ sessionLoader       = (*ProductAgent)(nil)
	_ sessionResumer      = (*ProductAgent)(nil)
	_ sessionCloser       = (*ProductAgent)(nil)
	_ sessionModeSetter   = (*ProductAgent)(nil)
	_ sessionConfigSetter = (*ProductAgent)(nil)
	_ sessionSteerer      = (*ProductAgent)(nil)
	_ commandProvider     = (*ProductAgent)(nil)
)

func (a *ProductAgent) Initialize(ctx context.Context, req protocolacp.InitializeRequest) (protocolacp.InitializeResponse, error) {
	return a.inner.Initialize(ctx, req)
}

func (a *ProductAgent) NewSession(ctx context.Context, req protocolacp.NewSessionRequest) (protocolacp.NewSessionResponse, error) {
	return a.inner.NewSession(ctx, req)
}

func (a *ProductAgent) Prompt(ctx context.Context, req protocolacp.PromptRequest, callbacks PromptCallbacks) (protocolacp.PromptResponse, error) {
	return a.inner.Prompt(ctx, req, callbacks)
}

func (a *ProductAgent) Cancel(ctx context.Context, req protocolacp.CancelNotification) error {
	return a.inner.Cancel(ctx, req)
}

func (a *ProductAgent) ListSessions(ctx context.Context, req protocolacp.SessionListRequest) (protocolacp.SessionListResponse, error) {
	return a.inner.ListSessions(ctx, req)
}

func (a *ProductAgent) LoadSession(ctx context.Context, req protocolacp.LoadSessionRequest, callbacks PromptCallbacks) (protocolacp.LoadSessionResponse, error) {
	return a.inner.LoadSession(ctx, req, callbacks)
}

func (a *ProductAgent) ResumeSession(ctx context.Context, req protocolacp.ResumeSessionRequest) (protocolacp.ResumeSessionResponse, error) {
	return a.inner.ResumeSession(ctx, req)
}

func (a *ProductAgent) CloseSession(ctx context.Context, req protocolacp.CloseSessionRequest) (protocolacp.CloseSessionResponse, error) {
	return a.inner.CloseSession(ctx, req)
}

func (a *ProductAgent) SetSessionMode(ctx context.Context, req protocolacp.SetSessionModeRequest) (protocolacp.SetSessionModeResponse, error) {
	return a.inner.SetSessionMode(ctx, req)
}

func (a *ProductAgent) SetSessionConfigOption(ctx context.Context, req protocolacp.SetSessionConfigOptionRequest) (protocolacp.SetSessionConfigOptionResponse, error) {
	return a.inner.SetSessionConfigOption(ctx, req)
}

func (a *ProductAgent) SteerSession(ctx context.Context, req protocolacp.SessionSteeringRequest) (protocolacp.SessionSteeringResponse, error) {
	return a.inner.SteerSession(ctx, req)
}

func (a *ProductAgent) AvailableCommands(ctx context.Context, sessionID string) ([]protocolacp.AvailableCommand, error) {
	return a.inner.AvailableCommands(ctx, sessionID)
}

type agentAuthenticator interface {
	Authenticate(context.Context, protocolacp.AuthenticateRequest) (protocolacp.AuthenticateResponse, error)
}

type sessionLister interface {
	ListSessions(context.Context, protocolacp.SessionListRequest) (protocolacp.SessionListResponse, error)
}

type sessionLoader interface {
	LoadSession(context.Context, protocolacp.LoadSessionRequest, PromptCallbacks) (protocolacp.LoadSessionResponse, error)
}

type sessionResumer interface {
	ResumeSession(context.Context, protocolacp.ResumeSessionRequest) (protocolacp.ResumeSessionResponse, error)
}

type sessionCloser interface {
	CloseSession(context.Context, protocolacp.CloseSessionRequest) (protocolacp.CloseSessionResponse, error)
}

type sessionModeSetter interface {
	SetSessionMode(context.Context, protocolacp.SetSessionModeRequest) (protocolacp.SetSessionModeResponse, error)
}

type sessionConfigSetter interface {
	SetSessionConfigOption(context.Context, protocolacp.SetSessionConfigOptionRequest) (protocolacp.SetSessionConfigOptionResponse, error)
}

type commandProvider interface {
	AvailableCommands(context.Context, string) ([]protocolacp.AvailableCommand, error)
}

type sessionSteerer interface {
	SteerSession(context.Context, protocolacp.SessionSteeringRequest) (protocolacp.SessionSteeringResponse, error)
}
