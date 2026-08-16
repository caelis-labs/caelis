package local

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	controladapter "github.com/caelis-labs/caelis/app/gatewayapp/controladapter"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

// AgentService is the local implementation of the focused AppServer Agent
// capability.
type AgentService struct {
	host     *gatewayapp.Stack
	commands appserver.AgentCommandService
}

func NewAgentService(host *gatewayapp.Stack) (*AgentService, error) {
	if host == nil || host.AgentCommands() == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: host Stack is required")
	}
	return &AgentService{host: host, commands: host.AgentCommands()}, nil
}

func (s *AgentService) ListAgents(ctx context.Context, principal appserver.Principal, req appserver.AgentRequest) ([]appserver.AgentCandidate, error) {
	driver, err := s.hostAdapter(principal, req.Surface)
	if err != nil {
		return nil, err
	}
	return driver.ListAgents(ctx, req.Limit)
}

func (s *AgentService) AgentStatus(ctx context.Context, principal appserver.Principal, req appserver.AgentRequest) (appserver.AgentStatusSnapshot, error) {
	if strings.TrimSpace(req.SessionID) == "" {
		driver, err := s.hostAdapter(principal, req.Surface)
		if err != nil {
			return appserver.AgentStatusSnapshot{}, err
		}
		return driver.AgentStatus(ctx)
	}
	driver, closeDriver, err := s.runtimeAdapter(ctx, principal, req.SessionID, req.Surface, false)
	if err != nil {
		return appserver.AgentStatusSnapshot{}, err
	}
	defer closeDriver()
	return driver.AgentStatus(ctx)
}

func (s *AgentService) HandoffAgent(ctx context.Context, principal appserver.Principal, req appserver.HandoffAgentRequest) (appserver.CommandResult, error) {
	kind := session.ControllerKindACP
	target := strings.TrimSpace(req.Target)
	if target == "" || strings.EqualFold(target, "main") || strings.EqualFold(target, "local") || strings.EqualFold(target, "kernel") {
		kind = session.ControllerKindKernel
		target = ""
	}
	return s.host.ControlClient().Handoff(ctx, principal, appserver.HandoffRequest{
		WriteBase: req.WriteBase,
		Kind:      kind, Agent: target, Source: "user_agent_handoff",
		Reason: "user selected controller",
	})
}

func (s *AgentService) PrepareACP(ctx context.Context, principal appserver.Principal, req appserver.PrepareACPRequest) (appserver.CommandResult, error) {
	return s.commands.PrepareACP(ctx, principal, req)
}

func (s *AgentService) PrepareACPAuthentication(ctx context.Context, principal appserver.Principal, req appserver.PrepareACPAuthenticationRequest) (appserver.CommandResult, error) {
	return s.commands.PrepareACPAuthentication(ctx, principal, req)
}

func (s *AgentService) ACPPreparation(ctx context.Context, principal appserver.Principal, req appserver.ACPPreparationRequest) (controlagents.ACPPreparation, error) {
	if err := authorizeHostCapability(principal); err != nil {
		return controlagents.ACPPreparation{}, err
	}
	return s.host.ACPPreparation(ctx, principal.ID, req.Ref)
}

func (s *AgentService) ConnectACP(ctx context.Context, principal appserver.Principal, req appserver.ConnectACPRequest) (appserver.CommandResult, error) {
	return s.commands.ConnectACP(ctx, principal, req)
}

func (s *AgentService) DisconnectCandidates(ctx context.Context, principal appserver.Principal, req appserver.AgentRequest) (appserver.DisconnectCandidatesSnapshot, error) {
	if err := authorizeHostCapability(principal); err != nil {
		return appserver.DisconnectCandidatesSnapshot{}, err
	}
	if strings.TrimSpace(req.SessionID) != "" {
		return appserver.DisconnectCandidatesSnapshot{}, errorcode.New(errorcode.InvalidArgument, "app/gatewayapp/controladapter/local: ACP disconnect candidates are Host-scoped")
	}
	return s.host.DisconnectCandidatesSnapshot(ctx)
}

func (s *AgentService) DisconnectACP(ctx context.Context, principal appserver.Principal, req appserver.DisconnectACPRequest) (appserver.CommandResult, error) {
	return s.commands.DisconnectACP(ctx, principal, req)
}

func (s *AgentService) AgentBindingStatus(ctx context.Context, principal appserver.Principal, req appserver.AgentRequest) (agentbinding.Status, error) {
	if err := authorizeHostCapability(principal); err != nil {
		return agentbinding.Status{}, err
	}
	if strings.TrimSpace(req.SessionID) != "" {
		return agentbinding.Status{}, errorcode.New(errorcode.InvalidArgument, "app/gatewayapp/controladapter/local: Agent binding status is Host-scoped")
	}
	return s.host.AgentBindings().AgentBindingStatus(ctx)
}

func (s *AgentService) BindAgentBinding(ctx context.Context, principal appserver.Principal, req appserver.BindAgentBindingRequest) (appserver.CommandResult, error) {
	return s.commands.BindAgentBinding(ctx, principal, req)
}

func (s *AgentService) ResetAgentBinding(ctx context.Context, principal appserver.Principal, req appserver.ResetAgentBindingRequest) (appserver.CommandResult, error) {
	return s.commands.ResetAgentBinding(ctx, principal, req)
}

func (s *AgentService) CreateAgentRole(ctx context.Context, principal appserver.Principal, req appserver.CreateAgentRoleRequest) (appserver.CommandResult, error) {
	return s.commands.CreateAgentRole(ctx, principal, req)
}

func (s *AgentService) DeleteAgentRole(ctx context.Context, principal appserver.Principal, req appserver.DeleteAgentRoleRequest) (appserver.CommandResult, error) {
	return s.commands.DeleteAgentRole(ctx, principal, req)
}

func (s *AgentService) SaveAgentBindingSet(ctx context.Context, principal appserver.Principal, req appserver.AgentBindingSetRequest) (appserver.CommandResult, error) {
	return s.commands.SaveAgentBindingSet(ctx, principal, req)
}

func (s *AgentService) ApplyAgentBindingSet(ctx context.Context, principal appserver.Principal, req appserver.AgentBindingSetRequest) (appserver.CommandResult, error) {
	return s.commands.ApplyAgentBindingSet(ctx, principal, req)
}

func (s *AgentService) DeleteAgentBindingSet(ctx context.Context, principal appserver.Principal, req appserver.AgentBindingSetRequest) (appserver.CommandResult, error) {
	return s.commands.DeleteAgentBindingSet(ctx, principal, req)
}

func (s *AgentService) hostAdapter(principal appserver.Principal, surface string) (controladapter.AgentAssembler, error) {
	if s == nil || s.host == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: Agent service is unavailable")
	}
	if err := authorizeHostCapability(principal); err != nil {
		return nil, err
	}
	return controladapter.NewAgentAssemblerForStack(runtimeStack(s.host), strings.TrimSpace(surface), ""), nil
}

func (s *AgentService) runtimeAdapter(ctx context.Context, principal appserver.Principal, sessionID, surface string, activate bool) (controladapter.AgentAssembler, func(), error) {
	lease, err := s.host.AcquireControlRuntime(ctx, principal, appserver.ActionSessionInspect, sessionID, activate)
	if err != nil {
		return nil, nil, err
	}
	driver, err := controladapter.NewAgentAssemblerForSession(ctx, runtimeStackFromView(lease.ControlRuntimeView()), lease.Session(), strings.TrimSpace(surface), "")
	if err != nil {
		_ = lease.Close(ctx)
		return nil, nil, err
	}
	return driver, func() { _ = lease.Close(context.Background()) }, nil
}

var _ appserver.AgentService = (*AgentService)(nil)
