package controlclient

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
)

type AgentRequest struct {
	SessionID string `json:"session_id"`
	Surface   string `json:"surface,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type HandoffAgentRequest struct {
	SessionID string `json:"session_id"`
	Surface   string `json:"surface,omitempty"`
	Target    string `json:"target"`
}

type ConnectACPRequest struct {
	SessionID string                       `json:"session_id"`
	Surface   string                       `json:"surface,omitempty"`
	Request   controlagents.ConnectRequest `json:"request"`
}

type DisconnectACPRequest struct {
	SessionID string `json:"session_id"`
	Surface   string `json:"surface,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
}

type AgentBindingRequest struct {
	SessionID string               `json:"session_id"`
	Binding   agentbinding.Binding `json:"binding"`
	Handle    agentbinding.Handle  `json:"handle,omitempty"`
	Role      agentbinding.Role    `json:"role"`
	SetName   string               `json:"set_name,omitempty"`
}

// AgentService is the principal-aware AppServer Agent roster, controller, and
// binding capability.
type AgentService interface {
	ListAgents(context.Context, Principal, AgentRequest) ([]AgentCandidate, error)
	AgentStatus(context.Context, Principal, AgentRequest) (AgentStatusSnapshot, error)
	HandoffAgent(context.Context, Principal, HandoffAgentRequest) (AgentStatusSnapshot, error)
	DiscoverACPConnection(context.Context, Principal, ConnectACPRequest) (controlagents.DiscoverySnapshot, error)
	ConnectACP(context.Context, Principal, ConnectACPRequest) (controlagents.ConnectResult, error)
	DisconnectCandidates(context.Context, Principal, DisconnectACPRequest) ([]controlagents.DisconnectCandidate, error)
	DisconnectACP(context.Context, Principal, DisconnectACPRequest) (controlagents.DisconnectResult, error)
	AgentBindingStatus(context.Context, Principal, AgentRequest) (agentbinding.Status, error)
	BindAgentBinding(context.Context, Principal, AgentBindingRequest) (agentbinding.Status, error)
	ResetAgentBinding(context.Context, Principal, AgentBindingRequest) (agentbinding.Status, error)
	CreateAgentRole(context.Context, Principal, AgentBindingRequest) (agentbinding.Status, error)
	DeleteAgentRole(context.Context, Principal, AgentBindingRequest) (agentbinding.Status, error)
	SaveAgentBindingSet(context.Context, Principal, AgentBindingRequest) (agentbinding.Status, error)
	ApplyAgentBindingSet(context.Context, Principal, AgentBindingRequest) (agentbinding.Status, error)
	DeleteAgentBindingSet(context.Context, Principal, AgentBindingRequest) (agentbinding.Status, error)
}

type AgentClient interface {
	ListAgents(context.Context, AgentRequest) ([]AgentCandidate, error)
	AgentStatus(context.Context, AgentRequest) (AgentStatusSnapshot, error)
	HandoffAgent(context.Context, HandoffAgentRequest) (AgentStatusSnapshot, error)
	DiscoverACPConnection(context.Context, ConnectACPRequest) (controlagents.DiscoverySnapshot, error)
	ConnectACP(context.Context, ConnectACPRequest) (controlagents.ConnectResult, error)
	DisconnectCandidates(context.Context, DisconnectACPRequest) ([]controlagents.DisconnectCandidate, error)
	DisconnectACP(context.Context, DisconnectACPRequest) (controlagents.DisconnectResult, error)
	AgentBindingStatus(context.Context, AgentRequest) (agentbinding.Status, error)
	BindAgentBinding(context.Context, AgentBindingRequest) (agentbinding.Status, error)
	ResetAgentBinding(context.Context, AgentBindingRequest) (agentbinding.Status, error)
	CreateAgentRole(context.Context, AgentBindingRequest) (agentbinding.Status, error)
	DeleteAgentRole(context.Context, AgentBindingRequest) (agentbinding.Status, error)
	SaveAgentBindingSet(context.Context, AgentBindingRequest) (agentbinding.Status, error)
	ApplyAgentBindingSet(context.Context, AgentBindingRequest) (agentbinding.Status, error)
	DeleteAgentBindingSet(context.Context, AgentBindingRequest) (agentbinding.Status, error)
}

type boundAgentClient struct {
	service   AgentService
	principal Principal
}

func BindAgentClient(service AgentService, principal Principal) (AgentClient, error) {
	if service == nil {
		return nil, errors.New("controlclient: Agent service is required")
	}
	principal.ID = strings.TrimSpace(principal.ID)
	if principal.ID == "" {
		return nil, errors.New("controlclient: principal ID is required")
	}
	principal.Roles = append([]string(nil), principal.Roles...)
	return &boundAgentClient{service: service, principal: principal}, nil
}

func (c *boundAgentClient) boundPrincipal() Principal {
	principal := c.principal
	principal.Roles = append([]string(nil), principal.Roles...)
	return principal
}
func (c *boundAgentClient) ListAgents(ctx context.Context, req AgentRequest) ([]AgentCandidate, error) {
	return c.service.ListAgents(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) AgentStatus(ctx context.Context, req AgentRequest) (AgentStatusSnapshot, error) {
	return c.service.AgentStatus(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) HandoffAgent(ctx context.Context, req HandoffAgentRequest) (AgentStatusSnapshot, error) {
	return c.service.HandoffAgent(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) DiscoverACPConnection(ctx context.Context, req ConnectACPRequest) (controlagents.DiscoverySnapshot, error) {
	return c.service.DiscoverACPConnection(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) ConnectACP(ctx context.Context, req ConnectACPRequest) (controlagents.ConnectResult, error) {
	return c.service.ConnectACP(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) DisconnectCandidates(ctx context.Context, req DisconnectACPRequest) ([]controlagents.DisconnectCandidate, error) {
	return c.service.DisconnectCandidates(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) DisconnectACP(ctx context.Context, req DisconnectACPRequest) (controlagents.DisconnectResult, error) {
	return c.service.DisconnectACP(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) AgentBindingStatus(ctx context.Context, req AgentRequest) (agentbinding.Status, error) {
	return c.service.AgentBindingStatus(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) BindAgentBinding(ctx context.Context, req AgentBindingRequest) (agentbinding.Status, error) {
	return c.service.BindAgentBinding(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) ResetAgentBinding(ctx context.Context, req AgentBindingRequest) (agentbinding.Status, error) {
	return c.service.ResetAgentBinding(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) CreateAgentRole(ctx context.Context, req AgentBindingRequest) (agentbinding.Status, error) {
	return c.service.CreateAgentRole(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) DeleteAgentRole(ctx context.Context, req AgentBindingRequest) (agentbinding.Status, error) {
	return c.service.DeleteAgentRole(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) SaveAgentBindingSet(ctx context.Context, req AgentBindingRequest) (agentbinding.Status, error) {
	return c.service.SaveAgentBindingSet(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) ApplyAgentBindingSet(ctx context.Context, req AgentBindingRequest) (agentbinding.Status, error) {
	return c.service.ApplyAgentBindingSet(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) DeleteAgentBindingSet(ctx context.Context, req AgentBindingRequest) (agentbinding.Status, error) {
	return c.service.DeleteAgentBindingSet(ctx, c.boundPrincipal(), req)
}

var _ AgentClient = (*boundAgentClient)(nil)
