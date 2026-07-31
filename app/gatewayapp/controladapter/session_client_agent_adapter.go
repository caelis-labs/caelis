package controladapter

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func (a *SessionClientAdapter) ListAgents(ctx context.Context, limit int) ([]controlprompt.AgentCandidate, error) {
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return nil, err
	}
	return a.agentClient.ListAgents(ctx, controlclient.AgentRequest{
		SessionID: state.SessionID, Surface: a.surface, Limit: limit,
	})
}

func (a *SessionClientAdapter) AgentStatus(ctx context.Context) (controlprompt.AgentStatusSnapshot, error) {
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return controlprompt.AgentStatusSnapshot{}, err
	}
	return a.agentClient.AgentStatus(ctx, controlclient.AgentRequest{SessionID: state.SessionID, Surface: a.surface})
}

func (a *SessionClientAdapter) HandoffAgent(ctx context.Context, target string) (controlprompt.AgentStatusSnapshot, error) {
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return controlprompt.AgentStatusSnapshot{}, err
	}
	return a.agentClient.HandoffAgent(ctx, controlclient.HandoffAgentRequest{
		SessionID: state.SessionID, Surface: a.surface, Target: strings.TrimSpace(target),
	})
}

func (a *SessionClientAdapter) DiscoverACPConnection(ctx context.Context, request controlagents.ConnectRequest) (controlagents.DiscoverySnapshot, error) {
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return controlagents.DiscoverySnapshot{}, err
	}
	return a.agentClient.DiscoverACPConnection(ctx, controlclient.ConnectACPRequest{
		SessionID: state.SessionID, Surface: a.surface, Request: request,
	})
}

func (a *SessionClientAdapter) ConnectACP(ctx context.Context, request controlagents.ConnectRequest) (controlagents.ConnectResult, error) {
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return controlagents.ConnectResult{}, err
	}
	return a.agentClient.ConnectACP(ctx, controlclient.ConnectACPRequest{
		SessionID: state.SessionID, Surface: a.surface, Request: request,
	})
}

func (a *SessionClientAdapter) DisconnectCandidates(ctx context.Context) ([]controlagents.DisconnectCandidate, error) {
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return nil, err
	}
	return a.agentClient.DisconnectCandidates(ctx, controlclient.DisconnectACPRequest{
		SessionID: state.SessionID, Surface: a.surface,
	})
}

func (a *SessionClientAdapter) DisconnectACP(ctx context.Context, agentID string) (controlagents.DisconnectResult, error) {
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return controlagents.DisconnectResult{}, err
	}
	return a.agentClient.DisconnectACP(ctx, controlclient.DisconnectACPRequest{
		SessionID: state.SessionID, Surface: a.surface, AgentID: strings.TrimSpace(agentID),
	})
}

func (a *SessionClientAdapter) AgentBindingStatus(ctx context.Context) (agentbinding.Status, error) {
	return a.withAgentBinding(ctx, controlclient.AgentBindingRequest{}, "status")
}

func (a *SessionClientAdapter) BindAgentBinding(ctx context.Context, binding agentbinding.Binding) (agentbinding.Status, error) {
	return a.withAgentBinding(ctx, controlclient.AgentBindingRequest{Binding: binding}, "bind")
}

func (a *SessionClientAdapter) ResetAgentBinding(ctx context.Context, handle agentbinding.Handle) (agentbinding.Status, error) {
	return a.withAgentBinding(ctx, controlclient.AgentBindingRequest{Handle: handle}, "reset")
}

func (a *SessionClientAdapter) withAgentBinding(ctx context.Context, request controlclient.AgentBindingRequest, action string) (agentbinding.Status, error) {
	if a == nil || a.agentClient == nil {
		return agentbinding.Status{}, errors.New("app/gatewayapp/controladapter: Agent client is unavailable")
	}
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return agentbinding.Status{}, err
	}
	request.SessionID = state.SessionID
	switch action {
	case "status":
		return a.agentClient.AgentBindingStatus(ctx, controlclient.AgentRequest{SessionID: state.SessionID, Surface: a.surface})
	case "bind":
		return a.agentClient.BindAgentBinding(ctx, request)
	case "reset":
		return a.agentClient.ResetAgentBinding(ctx, request)
	default:
		return agentbinding.Status{}, errors.New("app/gatewayapp/controladapter: unknown Agent binding action")
	}
}

func (a *SessionClientAdapter) CreateAgentRole(ctx context.Context, role agentbinding.Role, binding agentbinding.Binding) (agentbinding.Status, error) {
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return agentbinding.Status{}, err
	}
	return a.agentClient.CreateAgentRole(ctx, controlclient.AgentBindingRequest{SessionID: state.SessionID, Role: role, Binding: binding})
}

func (a *SessionClientAdapter) DeleteAgentRole(ctx context.Context, handle agentbinding.Handle) (agentbinding.Status, error) {
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return agentbinding.Status{}, err
	}
	return a.agentClient.DeleteAgentRole(ctx, controlclient.AgentBindingRequest{SessionID: state.SessionID, Handle: handle})
}

func (a *SessionClientAdapter) SaveAgentBindingSet(ctx context.Context, name string) (agentbinding.Status, error) {
	return a.agentBindingSet(ctx, name, "save")
}

func (a *SessionClientAdapter) ApplyAgentBindingSet(ctx context.Context, name string) (agentbinding.Status, error) {
	return a.agentBindingSet(ctx, name, "apply")
}

func (a *SessionClientAdapter) DeleteAgentBindingSet(ctx context.Context, name string) (agentbinding.Status, error) {
	return a.agentBindingSet(ctx, name, "delete")
}

func (a *SessionClientAdapter) agentBindingSet(ctx context.Context, name, action string) (agentbinding.Status, error) {
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return agentbinding.Status{}, err
	}
	req := controlclient.AgentBindingRequest{SessionID: state.SessionID, SetName: strings.TrimSpace(name)}
	switch action {
	case "save":
		return a.agentClient.SaveAgentBindingSet(ctx, req)
	case "apply":
		return a.agentClient.ApplyAgentBindingSet(ctx, req)
	case "delete":
		return a.agentClient.DeleteAgentBindingSet(ctx, req)
	default:
		return agentbinding.Status{}, errors.New("app/gatewayapp/controladapter: unknown Agent binding set action")
	}
}
