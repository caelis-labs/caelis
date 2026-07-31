package local

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	controladapter "github.com/caelis-labs/caelis/app/gatewayapp/controladapter"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controlclient "github.com/caelis-labs/caelis/control/client"
)

// AgentService is the local implementation of the focused AppServer Agent
// capability.
type AgentService struct {
	host *gatewayapp.Stack
}

func NewAgentService(host *gatewayapp.Stack) (*AgentService, error) {
	if host == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: host Stack is required")
	}
	return &AgentService{host: host}, nil
}

func (s *AgentService) ListAgents(ctx context.Context, principal controlclient.Principal, req controlclient.AgentRequest) ([]controlclient.AgentCandidate, error) {
	driver, closeDriver, err := s.runtimeAdapter(ctx, principal, req.SessionID, req.Surface, false)
	if err != nil {
		return nil, err
	}
	defer closeDriver()
	return driver.ListAgents(ctx, req.Limit)
}

func (s *AgentService) AgentStatus(ctx context.Context, principal controlclient.Principal, req controlclient.AgentRequest) (controlclient.AgentStatusSnapshot, error) {
	driver, closeDriver, err := s.runtimeAdapter(ctx, principal, req.SessionID, req.Surface, false)
	if err != nil {
		return controlclient.AgentStatusSnapshot{}, err
	}
	defer closeDriver()
	return driver.AgentStatus(ctx)
}

func (s *AgentService) HandoffAgent(ctx context.Context, principal controlclient.Principal, req controlclient.HandoffAgentRequest) (controlclient.AgentStatusSnapshot, error) {
	state, err := s.host.ControlClient().InspectSession(ctx, principal, controlclient.StateRequest{SessionID: req.SessionID})
	if err != nil {
		return controlclient.AgentStatusSnapshot{}, err
	}
	revision := state.Revision
	kind := session.ControllerKindACP
	target := strings.TrimSpace(req.Target)
	if target == "" || strings.EqualFold(target, "main") || strings.EqualFold(target, "local") || strings.EqualFold(target, "kernel") {
		kind = session.ControllerKindKernel
		target = ""
	}
	_, err = s.host.ControlClient().Handoff(ctx, principal, controlclient.HandoffRequest{
		WriteBase: controlclient.WriteBase{
			OperationID: "controller-handoff-" + uuid.NewString(), SessionID: state.SessionID,
			ExpectedRevision: &revision, ExpectedControllerEpoch: state.Controller.EpochID,
		},
		Kind: kind, Agent: target, Source: "user_agent_handoff",
		Reason: "user selected controller",
	})
	if err != nil {
		return controlclient.AgentStatusSnapshot{}, err
	}
	return s.AgentStatus(ctx, principal, controlclient.AgentRequest{SessionID: req.SessionID, Surface: req.Surface})
}

func (s *AgentService) DiscoverACPConnection(ctx context.Context, principal controlclient.Principal, req controlclient.ConnectACPRequest) (controlagents.DiscoverySnapshot, error) {
	driver, err := s.hostAdapter(ctx, principal, req.SessionID, req.Surface)
	if err != nil {
		return controlagents.DiscoverySnapshot{}, err
	}
	return driver.DiscoverACPConnection(ctx, req.Request)
}

func (s *AgentService) ConnectACP(ctx context.Context, principal controlclient.Principal, req controlclient.ConnectACPRequest) (controlagents.ConnectResult, error) {
	driver, err := s.hostAdapter(ctx, principal, req.SessionID, req.Surface)
	if err != nil {
		return controlagents.ConnectResult{}, err
	}
	return driver.ConnectACP(ctx, req.Request)
}

func (s *AgentService) DisconnectCandidates(ctx context.Context, principal controlclient.Principal, req controlclient.DisconnectACPRequest) ([]controlagents.DisconnectCandidate, error) {
	driver, err := s.hostAdapter(ctx, principal, req.SessionID, req.Surface)
	if err != nil {
		return nil, err
	}
	return driver.DisconnectCandidates(ctx)
}

func (s *AgentService) DisconnectACP(ctx context.Context, principal controlclient.Principal, req controlclient.DisconnectACPRequest) (controlagents.DisconnectResult, error) {
	driver, err := s.hostAdapter(ctx, principal, req.SessionID, req.Surface)
	if err != nil {
		return controlagents.DisconnectResult{}, err
	}
	return driver.DisconnectACP(ctx, req.AgentID)
}

func (s *AgentService) AgentBindingStatus(ctx context.Context, principal controlclient.Principal, req controlclient.AgentRequest) (agentbinding.Status, error) {
	if _, err := s.authorizedSession(ctx, principal, req.SessionID); err != nil {
		return agentbinding.Status{}, err
	}
	return s.host.AgentBindings().AgentBindingStatus(ctx)
}

func (s *AgentService) BindAgentBinding(ctx context.Context, principal controlclient.Principal, req controlclient.AgentBindingRequest) (agentbinding.Status, error) {
	if _, err := s.authorizedSession(ctx, principal, req.SessionID); err != nil {
		return agentbinding.Status{}, err
	}
	return s.host.AgentBindings().BindAgentBinding(ctx, req.Binding)
}

func (s *AgentService) ResetAgentBinding(ctx context.Context, principal controlclient.Principal, req controlclient.AgentBindingRequest) (agentbinding.Status, error) {
	if _, err := s.authorizedSession(ctx, principal, req.SessionID); err != nil {
		return agentbinding.Status{}, err
	}
	return s.host.AgentBindings().ResetAgentBinding(ctx, req.Handle)
}

func (s *AgentService) CreateAgentRole(ctx context.Context, principal controlclient.Principal, req controlclient.AgentBindingRequest) (agentbinding.Status, error) {
	if _, err := s.authorizedSession(ctx, principal, req.SessionID); err != nil {
		return agentbinding.Status{}, err
	}
	return s.host.AgentBindings().CreateAgentRole(ctx, req.Role, req.Binding)
}

func (s *AgentService) DeleteAgentRole(ctx context.Context, principal controlclient.Principal, req controlclient.AgentBindingRequest) (agentbinding.Status, error) {
	if _, err := s.authorizedSession(ctx, principal, req.SessionID); err != nil {
		return agentbinding.Status{}, err
	}
	return s.host.AgentBindings().DeleteAgentRole(ctx, req.Handle)
}

func (s *AgentService) SaveAgentBindingSet(ctx context.Context, principal controlclient.Principal, req controlclient.AgentBindingRequest) (agentbinding.Status, error) {
	if _, err := s.authorizedSession(ctx, principal, req.SessionID); err != nil {
		return agentbinding.Status{}, err
	}
	return s.host.AgentBindings().SaveAgentBindingSet(ctx, req.SetName)
}

func (s *AgentService) ApplyAgentBindingSet(ctx context.Context, principal controlclient.Principal, req controlclient.AgentBindingRequest) (agentbinding.Status, error) {
	if _, err := s.authorizedSession(ctx, principal, req.SessionID); err != nil {
		return agentbinding.Status{}, err
	}
	return s.host.AgentBindings().ApplyAgentBindingSet(ctx, req.SetName)
}

func (s *AgentService) DeleteAgentBindingSet(ctx context.Context, principal controlclient.Principal, req controlclient.AgentBindingRequest) (agentbinding.Status, error) {
	if _, err := s.authorizedSession(ctx, principal, req.SessionID); err != nil {
		return agentbinding.Status{}, err
	}
	return s.host.AgentBindings().DeleteAgentBindingSet(ctx, req.SetName)
}

func (s *AgentService) authorizedSession(ctx context.Context, principal controlclient.Principal, sessionID string) (session.Session, error) {
	if s == nil || s.host == nil || s.host.Sessions == nil {
		return session.Session{}, errors.New("app/gatewayapp/controladapter/local: Agent service is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if err := (controlclient.SessionAuthorizer{Sessions: s.host.Sessions}).Authorize(ctx, principal, controlclient.ActionSessionInspect, sessionID); err != nil {
		return session.Session{}, err
	}
	return s.host.Sessions.Session(ctx, session.SessionRef{SessionID: sessionID})
}

func (s *AgentService) hostAdapter(ctx context.Context, principal controlclient.Principal, sessionID, surface string) (controladapter.AgentAssembler, error) {
	active, err := s.authorizedSession(ctx, principal, sessionID)
	if err != nil {
		return nil, err
	}
	return controladapter.NewAgentAssemblerForSession(ctx, runtimeStack(s.host), active, strings.TrimSpace(surface), "")
}

func (s *AgentService) runtimeAdapter(ctx context.Context, principal controlclient.Principal, sessionID, surface string, activate bool) (controladapter.AgentAssembler, func(), error) {
	lease, err := s.host.AcquireControlRuntime(ctx, principal, sessionID, activate)
	if err != nil {
		return nil, nil, err
	}
	driver, err := controladapter.NewAgentAssemblerForSession(ctx, runtimeStack(lease.Runtime()), lease.Session(), strings.TrimSpace(surface), "")
	if err != nil {
		_ = lease.Close(ctx)
		return nil, nil, err
	}
	return driver, func() { _ = lease.Close(context.Background()) }, nil
}

var _ controlclient.AgentService = (*AgentService)(nil)
