package appserver

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
)

type AgentRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Surface   string `json:"surface,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type HandoffAgentRequest struct {
	WriteBase
	Target string `json:"target"`
}

type PrepareACPRequest struct {
	WriteBase
	Request controlagents.ACPPrepareRequest `json:"request"`
}

type PrepareACPAuthenticationRequest struct {
	WriteBase
	PreparationRef    string `json:"preparation_ref"`
	PreparationDigest string `json:"preparation_digest"`
	MethodID          string `json:"method_id"`
}

type ConnectACPRequest struct {
	WriteBase
	PreparationRef    string            `json:"preparation_ref"`
	PreparationDigest string            `json:"preparation_digest"`
	ConfigValues      map[string]string `json:"config_values,omitempty"`
}

type ACPPreparationRequest struct {
	Ref string `json:"ref"`
}

type DisconnectACPRequest struct {
	WriteBase
	AgentID string `json:"agent_id"`
}

// DisconnectCandidatesSnapshot binds the removable external-Agent roster to
// the exact Host configuration revision used by a subsequent disconnect CAS.
type DisconnectCandidatesSnapshot struct {
	Revision   uint64                              `json:"revision"`
	Candidates []controlagents.DisconnectCandidate `json:"candidates"`
}

type BindAgentBindingRequest struct {
	WriteBase
	Binding agentbinding.Binding `json:"binding"`
}

type ResetAgentBindingRequest struct {
	WriteBase
	Handle agentbinding.Handle `json:"handle"`
}

type CreateAgentRoleRequest struct {
	WriteBase
	Role    agentbinding.Role    `json:"role"`
	Binding agentbinding.Binding `json:"binding,omitempty"`
}

type DeleteAgentRoleRequest struct {
	WriteBase
	Handle agentbinding.Handle `json:"handle"`
}

type AgentBindingSetRequest struct {
	WriteBase
	SetName string `json:"set_name"`
}

// AgentService is the principal-aware AppServer Agent roster, controller, and
// binding capability.
type AgentService interface {
	ListAgents(context.Context, Principal, AgentRequest) ([]AgentCandidate, error)
	AgentStatus(context.Context, Principal, AgentRequest) (AgentStatusSnapshot, error)
	HandoffAgent(context.Context, Principal, HandoffAgentRequest) (CommandResult, error)
	PrepareACP(context.Context, Principal, PrepareACPRequest) (CommandResult, error)
	PrepareACPAuthentication(context.Context, Principal, PrepareACPAuthenticationRequest) (CommandResult, error)
	ACPPreparation(context.Context, Principal, ACPPreparationRequest) (controlagents.ACPPreparation, error)
	ConnectACP(context.Context, Principal, ConnectACPRequest) (CommandResult, error)
	DisconnectCandidates(context.Context, Principal, AgentRequest) (DisconnectCandidatesSnapshot, error)
	DisconnectACP(context.Context, Principal, DisconnectACPRequest) (CommandResult, error)
	AgentBindingStatus(context.Context, Principal, AgentRequest) (agentbinding.Status, error)
	BindAgentBinding(context.Context, Principal, BindAgentBindingRequest) (CommandResult, error)
	ResetAgentBinding(context.Context, Principal, ResetAgentBindingRequest) (CommandResult, error)
	CreateAgentRole(context.Context, Principal, CreateAgentRoleRequest) (CommandResult, error)
	DeleteAgentRole(context.Context, Principal, DeleteAgentRoleRequest) (CommandResult, error)
	SaveAgentBindingSet(context.Context, Principal, AgentBindingSetRequest) (CommandResult, error)
	ApplyAgentBindingSet(context.Context, Principal, AgentBindingSetRequest) (CommandResult, error)
	DeleteAgentBindingSet(context.Context, Principal, AgentBindingSetRequest) (CommandResult, error)
}

// AgentCommandService is the principal-aware Host Agent-binding mutation
// capability implemented by the shared Control command executor. It reuses the
// same durable operation ledger as Session and configuration commands.
type AgentCommandService interface {
	BindAgentBinding(context.Context, Principal, BindAgentBindingRequest) (CommandResult, error)
	ResetAgentBinding(context.Context, Principal, ResetAgentBindingRequest) (CommandResult, error)
	CreateAgentRole(context.Context, Principal, CreateAgentRoleRequest) (CommandResult, error)
	DeleteAgentRole(context.Context, Principal, DeleteAgentRoleRequest) (CommandResult, error)
	SaveAgentBindingSet(context.Context, Principal, AgentBindingSetRequest) (CommandResult, error)
	ApplyAgentBindingSet(context.Context, Principal, AgentBindingSetRequest) (CommandResult, error)
	DeleteAgentBindingSet(context.Context, Principal, AgentBindingSetRequest) (CommandResult, error)
	PrepareACP(context.Context, Principal, PrepareACPRequest) (CommandResult, error)
	PrepareACPAuthentication(context.Context, Principal, PrepareACPAuthenticationRequest) (CommandResult, error)
	ConnectACP(context.Context, Principal, ConnectACPRequest) (CommandResult, error)
	DisconnectACP(context.Context, Principal, DisconnectACPRequest) (CommandResult, error)
}

type AgentClient interface {
	ListAgents(context.Context, AgentRequest) ([]AgentCandidate, error)
	AgentStatus(context.Context, AgentRequest) (AgentStatusSnapshot, error)
	HandoffAgent(context.Context, HandoffAgentRequest) (CommandResult, error)
	PrepareACP(context.Context, PrepareACPRequest) (CommandResult, error)
	PrepareACPAuthentication(context.Context, PrepareACPAuthenticationRequest) (CommandResult, error)
	ACPPreparation(context.Context, ACPPreparationRequest) (controlagents.ACPPreparation, error)
	ConnectACP(context.Context, ConnectACPRequest) (CommandResult, error)
	DisconnectCandidates(context.Context, AgentRequest) (DisconnectCandidatesSnapshot, error)
	DisconnectACP(context.Context, DisconnectACPRequest) (CommandResult, error)
	AgentBindingStatus(context.Context, AgentRequest) (agentbinding.Status, error)
	BindAgentBinding(context.Context, BindAgentBindingRequest) (CommandResult, error)
	ResetAgentBinding(context.Context, ResetAgentBindingRequest) (CommandResult, error)
	CreateAgentRole(context.Context, CreateAgentRoleRequest) (CommandResult, error)
	DeleteAgentRole(context.Context, DeleteAgentRoleRequest) (CommandResult, error)
	SaveAgentBindingSet(context.Context, AgentBindingSetRequest) (CommandResult, error)
	ApplyAgentBindingSet(context.Context, AgentBindingSetRequest) (CommandResult, error)
	DeleteAgentBindingSet(context.Context, AgentBindingSetRequest) (CommandResult, error)
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
func (c *boundAgentClient) HandoffAgent(ctx context.Context, req HandoffAgentRequest) (CommandResult, error) {
	return c.service.HandoffAgent(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) PrepareACP(ctx context.Context, req PrepareACPRequest) (CommandResult, error) {
	return c.service.PrepareACP(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) PrepareACPAuthentication(ctx context.Context, req PrepareACPAuthenticationRequest) (CommandResult, error) {
	return c.service.PrepareACPAuthentication(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) ACPPreparation(ctx context.Context, req ACPPreparationRequest) (controlagents.ACPPreparation, error) {
	return c.service.ACPPreparation(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) ConnectACP(ctx context.Context, req ConnectACPRequest) (CommandResult, error) {
	return c.service.ConnectACP(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) DisconnectCandidates(ctx context.Context, req AgentRequest) (DisconnectCandidatesSnapshot, error) {
	return c.service.DisconnectCandidates(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) DisconnectACP(ctx context.Context, req DisconnectACPRequest) (CommandResult, error) {
	return c.service.DisconnectACP(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) AgentBindingStatus(ctx context.Context, req AgentRequest) (agentbinding.Status, error) {
	return c.service.AgentBindingStatus(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) BindAgentBinding(ctx context.Context, req BindAgentBindingRequest) (CommandResult, error) {
	return c.service.BindAgentBinding(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) ResetAgentBinding(ctx context.Context, req ResetAgentBindingRequest) (CommandResult, error) {
	return c.service.ResetAgentBinding(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) CreateAgentRole(ctx context.Context, req CreateAgentRoleRequest) (CommandResult, error) {
	return c.service.CreateAgentRole(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) DeleteAgentRole(ctx context.Context, req DeleteAgentRoleRequest) (CommandResult, error) {
	return c.service.DeleteAgentRole(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) SaveAgentBindingSet(ctx context.Context, req AgentBindingSetRequest) (CommandResult, error) {
	return c.service.SaveAgentBindingSet(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) ApplyAgentBindingSet(ctx context.Context, req AgentBindingSetRequest) (CommandResult, error) {
	return c.service.ApplyAgentBindingSet(ctx, c.boundPrincipal(), req)
}
func (c *boundAgentClient) DeleteAgentBindingSet(ctx context.Context, req AgentBindingSetRequest) (CommandResult, error) {
	return c.service.DeleteAgentBindingSet(ctx, c.boundPrincipal(), req)
}

var _ AgentClient = (*boundAgentClient)(nil)
