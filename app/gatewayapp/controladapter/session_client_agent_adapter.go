package controladapter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func (a *SessionClientAdapter) ListAgents(ctx context.Context, limit int) ([]controlprompt.AgentCandidate, error) {
	return a.agentClient.ListAgents(ctx, controlclient.AgentRequest{
		Surface: a.surface, Limit: limit,
	})
}

func (a *SessionClientAdapter) AgentStatus(ctx context.Context) (controlprompt.AgentStatusSnapshot, error) {
	sessionID := a.clientSessionID()
	status, err := a.agentClient.AgentStatus(ctx, controlclient.AgentRequest{SessionID: sessionID, Surface: a.surface})
	if err != nil && sessionID != "" {
		return a.agentClient.AgentStatus(ctx, controlclient.AgentRequest{Surface: a.surface})
	}
	return status, err
}

func (a *SessionClientAdapter) DisconnectCandidates(ctx context.Context) ([]controlagents.DisconnectCandidate, error) {
	snapshot, err := a.agentClient.DisconnectCandidates(ctx, controlclient.AgentRequest{Surface: a.surface})
	return snapshot.Candidates, err
}

func (a *SessionClientAdapter) DisconnectACP(ctx context.Context, agentID string) (controlagents.DisconnectResult, error) {
	if a == nil || a.agentClient == nil {
		return controlagents.DisconnectResult{}, errors.New("app/gatewayapp/controladapter: Agent client is unavailable")
	}
	agentID = strings.TrimSpace(agentID)
	snapshot, err := a.agentClient.DisconnectCandidates(ctx, controlclient.AgentRequest{Surface: a.surface})
	if err != nil {
		return controlagents.DisconnectResult{}, fmt.Errorf("app/gatewayapp/controladapter: read ACP disconnect candidates: %w", err)
	}
	var selected *controlagents.DisconnectCandidate
	for index := range snapshot.Candidates {
		if strings.EqualFold(strings.TrimSpace(snapshot.Candidates[index].AgentID), agentID) {
			candidate := snapshot.Candidates[index]
			selected = &candidate
			break
		}
	}
	if selected == nil {
		return controlagents.DisconnectResult{}, fmt.Errorf("app/gatewayapp/controladapter: external ACP Agent %q is not connected", agentID)
	}
	revision := snapshot.Revision
	result, commandErr := a.agentClient.DisconnectACP(ctx, controlclient.DisconnectACPRequest{
		WriteBase: controlclient.WriteBase{
			OperationID:      "agent-acp-disconnect-" + uuid.NewString(),
			ExpectedRevision: &revision,
		},
		AgentID: selected.AgentID,
	})
	if result.Outcome != controlclient.OutcomeCommitted {
		if commandErr == nil {
			commandErr = fmt.Errorf(
				"app/gatewayapp/controladapter: ACP disconnect outcome is %q: %s",
				result.Outcome,
				strings.TrimSpace(result.Detail),
			)
		}
		return controlagents.DisconnectResult{}, &controlclient.CommandReceiptError{Receipt: result, Err: commandErr}
	}
	if commandErr != nil {
		return controlagents.DisconnectResult{}, &controlclient.CommandReceiptError{Receipt: result, Err: commandErr}
	}
	disconnected := controlagents.DisconnectResult{
		Agent: controlagents.Agent{
			ID:           selected.AgentID,
			Name:         selected.Name,
			ConnectionID: selected.ConnectionID,
		},
		ConnectionID:      selected.ConnectionID,
		ConnectionRemoved: selected.LastOnConnection,
	}
	if detail := strings.TrimSpace(result.Detail); detail != "" {
		warning := fmt.Errorf(
			"app/gatewayapp/controladapter: ACP disconnect committed as operation %q with a warning; do not retry blindly: %s",
			strings.TrimSpace(result.OperationID),
			detail,
		)
		return disconnected, &controlclient.CommandReceiptError{Receipt: result, Err: warning}
	}
	return disconnected, nil
}

func (a *SessionClientAdapter) AgentBindingStatus(ctx context.Context) (agentbinding.Status, error) {
	if a == nil || a.agentClient == nil {
		return agentbinding.Status{}, errors.New("app/gatewayapp/controladapter: Agent client is unavailable")
	}
	return a.agentClient.AgentBindingStatus(ctx, controlclient.AgentRequest{Surface: a.surface})
}

func (a *SessionClientAdapter) BindAgentBinding(ctx context.Context, binding agentbinding.Binding) (agentbinding.Status, error) {
	return a.runAgentBindingMutation(ctx, "Agent binding", "agent-binding", func(base controlclient.WriteBase) (controlclient.CommandResult, error) {
		return a.agentClient.BindAgentBinding(ctx, controlclient.BindAgentBindingRequest{WriteBase: base, Binding: binding})
	})
}

func (a *SessionClientAdapter) ResetAgentBinding(ctx context.Context, handle agentbinding.Handle) (agentbinding.Status, error) {
	return a.runAgentBindingMutation(ctx, "Agent binding reset", "agent-binding-reset", func(base controlclient.WriteBase) (controlclient.CommandResult, error) {
		return a.agentClient.ResetAgentBinding(ctx, controlclient.ResetAgentBindingRequest{WriteBase: base, Handle: handle})
	})
}

func (a *SessionClientAdapter) CreateAgentRole(ctx context.Context, role agentbinding.Role, binding agentbinding.Binding) (agentbinding.Status, error) {
	return a.runAgentBindingMutation(ctx, "Agent role creation", "agent-role-create", func(base controlclient.WriteBase) (controlclient.CommandResult, error) {
		return a.agentClient.CreateAgentRole(ctx, controlclient.CreateAgentRoleRequest{WriteBase: base, Role: role, Binding: binding})
	})
}

func (a *SessionClientAdapter) DeleteAgentRole(ctx context.Context, handle agentbinding.Handle) (agentbinding.Status, error) {
	return a.runAgentBindingMutation(ctx, "Agent role deletion", "agent-role-delete", func(base controlclient.WriteBase) (controlclient.CommandResult, error) {
		return a.agentClient.DeleteAgentRole(ctx, controlclient.DeleteAgentRoleRequest{WriteBase: base, Handle: handle})
	})
}

func (a *SessionClientAdapter) SaveAgentBindingSet(ctx context.Context, name string) (agentbinding.Status, error) {
	return a.runAgentBindingSetMutation(ctx, "Agent binding-set save", "agent-binding-set-save", name, a.agentClient.SaveAgentBindingSet)
}

func (a *SessionClientAdapter) ApplyAgentBindingSet(ctx context.Context, name string) (agentbinding.Status, error) {
	return a.runAgentBindingSetMutation(ctx, "Agent binding-set apply", "agent-binding-set-apply", name, a.agentClient.ApplyAgentBindingSet)
}

func (a *SessionClientAdapter) DeleteAgentBindingSet(ctx context.Context, name string) (agentbinding.Status, error) {
	return a.runAgentBindingSetMutation(ctx, "Agent binding-set deletion", "agent-binding-set-delete", name, a.agentClient.DeleteAgentBindingSet)
}

func (a *SessionClientAdapter) runAgentBindingSetMutation(
	ctx context.Context,
	label string,
	operationPrefix string,
	name string,
	command func(context.Context, controlclient.AgentBindingSetRequest) (controlclient.CommandResult, error),
) (agentbinding.Status, error) {
	return a.runAgentBindingMutation(ctx, label, operationPrefix, func(base controlclient.WriteBase) (controlclient.CommandResult, error) {
		return command(ctx, controlclient.AgentBindingSetRequest{WriteBase: base, SetName: strings.TrimSpace(name)})
	})
}

func (a *SessionClientAdapter) runAgentBindingMutation(
	ctx context.Context,
	label string,
	operationPrefix string,
	command func(controlclient.WriteBase) (controlclient.CommandResult, error),
) (agentbinding.Status, error) {
	if a == nil || a.agentClient == nil || a.statusClient == nil {
		return agentbinding.Status{}, errors.New("app/gatewayapp/controladapter: Agent binding clients are unavailable")
	}
	before, err := a.addressedStatus(ctx, "", false)
	if err != nil {
		return agentbinding.Status{}, fmt.Errorf("app/gatewayapp/controladapter: read Host configuration revision: %w", err)
	}
	revision := before.Configuration.Revision
	result, commandErr := command(controlclient.WriteBase{
		OperationID:      operationPrefix + "-" + uuid.NewString(),
		ExpectedRevision: &revision,
	})
	if result.Outcome != controlclient.OutcomeCommitted {
		if commandErr == nil {
			commandErr = fmt.Errorf(
				"app/gatewayapp/controladapter: %s outcome is %q: %s",
				label,
				result.Outcome,
				strings.TrimSpace(result.Detail),
			)
		}
		return agentbinding.Status{}, &controlclient.CommandReceiptError{Receipt: result, Err: commandErr}
	}
	observed, observationErr := a.agentClient.AgentBindingStatus(ctx, controlclient.AgentRequest{Surface: a.surface})
	if observationErr != nil {
		observationErr = fmt.Errorf(
			"app/gatewayapp/controladapter: %s committed as operation %q but Agent binding status observation failed; do not retry blindly: %w",
			label,
			result.OperationID,
			observationErr,
		)
	}
	resultErr := errors.Join(commandErr, observationErr)
	if resultErr != nil {
		resultErr = &controlclient.CommandReceiptError{Receipt: result, Err: resultErr}
	}
	return observed, resultErr
}
