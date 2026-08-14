package controlclient

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

// SessionClient is the principal-bound Control contract consumed by a
// presentation client. Authentication binds the principal before this
// interface; callers cannot choose or forward another principal.
//
// SessionClient covers Session lifecycle, observation, and main-Turn commands.
// Participant execution and Task observation remain separate focused clients;
// the complete AppServer client set provides the same contracts through
// embedded and HTTP transports.
type SessionClient interface {
	Initialize(context.Context) (ServerInfo, error)
	ListSessions(context.Context, ListSessionsRequest) (session.SessionList, error)
	CreateSession(context.Context, CreateSessionRequest) (CommandResult, error)
	CloseSession(context.Context, CloseSessionRequest) (CommandResult, error)
	CompactSession(context.Context, CompactSessionRequest) (CommandResult, error)
	InspectSession(context.Context, StateRequest) (SessionState, error)
	Reconnect(context.Context, ReconnectRequest) (ReconnectResult, error)
	Prompt(context.Context, PromptRequest) (CommandResult, error)
	Steer(context.Context, SteerRequest) (CommandResult, error)
	Cancel(context.Context, CancelRequest) (CommandResult, error)
	ResolveApproval(context.Context, ResolveApprovalRequest) (CommandResult, error)
}

type boundSessionClient struct {
	service   Service
	principal Principal
}

// BindSessionClient binds one trusted principal to the in-process Control
// Service and exposes the same client contract as remote transports.
func BindSessionClient(service Service, principal Principal) (SessionClient, error) {
	if service == nil {
		return nil, errors.New("controlclient: service is required")
	}
	principal.ID = strings.TrimSpace(principal.ID)
	if principal.ID == "" {
		return nil, errors.New("controlclient: principal ID is required")
	}
	principal.Roles = append([]string(nil), principal.Roles...)
	return &boundSessionClient{service: service, principal: principal}, nil
}

func (c *boundSessionClient) Initialize(ctx context.Context) (ServerInfo, error) {
	if err := ctx.Err(); err != nil {
		return ServerInfo{}, err
	}
	return ServerInfo{
		ProtocolVersion: schema.CurrentProtocolVersion,
		EnvelopeVersion: EnvelopeVersion,
		APIVersion:      HTTPAPIVersion,
		ServerID:        ServerIdentity,
		Capabilities:    RequiredManagedHostCapabilities(),
		Transports:      []string{"embedded"},
	}, nil
}

func (c *boundSessionClient) ListSessions(ctx context.Context, request ListSessionsRequest) (session.SessionList, error) {
	return c.service.ListSessions(ctx, c.boundPrincipal(), request)
}

func (c *boundSessionClient) CreateSession(ctx context.Context, request CreateSessionRequest) (CommandResult, error) {
	return c.service.CreateSession(ctx, c.boundPrincipal(), request)
}

func (c *boundSessionClient) CloseSession(ctx context.Context, request CloseSessionRequest) (CommandResult, error) {
	return c.service.CloseSession(ctx, c.boundPrincipal(), request)
}

func (c *boundSessionClient) CompactSession(ctx context.Context, request CompactSessionRequest) (CommandResult, error) {
	return c.service.CompactSession(ctx, c.boundPrincipal(), request)
}

func (c *boundSessionClient) InspectSession(ctx context.Context, request StateRequest) (SessionState, error) {
	return c.service.InspectSession(ctx, c.boundPrincipal(), request)
}

func (c *boundSessionClient) Reconnect(ctx context.Context, request ReconnectRequest) (ReconnectResult, error) {
	return c.service.Reconnect(ctx, c.boundPrincipal(), request)
}

func (c *boundSessionClient) Prompt(ctx context.Context, request PromptRequest) (CommandResult, error) {
	return c.service.Prompt(ctx, c.boundPrincipal(), request)
}

func (c *boundSessionClient) Steer(ctx context.Context, request SteerRequest) (CommandResult, error) {
	return c.service.Steer(ctx, c.boundPrincipal(), request)
}

func (c *boundSessionClient) Cancel(ctx context.Context, request CancelRequest) (CommandResult, error) {
	return c.service.Cancel(ctx, c.boundPrincipal(), request)
}

func (c *boundSessionClient) ResolveApproval(ctx context.Context, request ResolveApprovalRequest) (CommandResult, error) {
	return c.service.ResolveApproval(ctx, c.boundPrincipal(), request)
}

func (c *boundSessionClient) boundPrincipal() Principal {
	principal := c.principal
	principal.Roles = append([]string(nil), principal.Roles...)
	return principal
}

var _ SessionClient = (*boundSessionClient)(nil)
