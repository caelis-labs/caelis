package acp

import (
	"context"
	"strings"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	runtimeacp "github.com/caelis-labs/caelis/internal/acpagentbridge"
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
	agent, err := runtimeacp.NewGatewayAgent(runtimeacp.GatewayAgentConfig{
		Clients:                    cfg.Clients,
		SystemSessionClient:        cfg.SystemSessionClient,
		AppName:                    cfg.AppName,
		UserID:                     cfg.UserID,
		WorkspaceKey:               strings.TrimSpace(cfg.WorkspaceKey),
		WorkspaceCWD:               strings.TrimSpace(cfg.WorkspaceCWD),
		ManagedSessionHistoryToken: strings.TrimSpace(cfg.ManagedSessionHistoryToken),
		SlashResultFormatter:       promptview.FormatSlashResult,
	})
	if err != nil {
		return nil, err
	}
	return &ProductAgent{inner: agent}, nil
}

// Agent is the product ACP surface consumed by ServeStdio. Its callback-aware
// prompt and load methods preserve Caelis's per-connection update and approval
// routing while the wire connection itself remains owned by acp-go-sdk.
type Agent interface {
	Initialize(context.Context, protocolacp.InitializeRequest) (protocolacp.InitializeResponse, error)
	NewSession(context.Context, protocolacp.NewSessionRequest) (protocolacp.NewSessionResponse, error)
	Prompt(context.Context, protocolacp.PromptRequest, PromptCallbacks) (protocolacp.PromptResponse, error)
	Cancel(context.Context, acpsdk.CancelNotification) error
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

func (a *ProductAgent) Cancel(ctx context.Context, req acpsdk.CancelNotification) error {
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
