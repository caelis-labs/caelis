package appserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
)

func (s *CommandService) BindAgentBinding(ctx context.Context, principal Principal, req BindAgentBindingRequest) (CommandResult, error) {
	target := "host/configuration/agent-bindings/handle/" + string(agentbinding.NormalizeHandle(req.Binding.Handle))
	return s.execute(ctx, principal, ActionAgentBindingBind, req.WriteBase, target, req)
}

func (s *CommandService) ResetAgentBinding(ctx context.Context, principal Principal, req ResetAgentBindingRequest) (CommandResult, error) {
	target := "host/configuration/agent-bindings/handle/" + string(agentbinding.NormalizeHandle(req.Handle))
	return s.execute(ctx, principal, ActionAgentBindingReset, req.WriteBase, target, req)
}

func (s *CommandService) CreateAgentRole(ctx context.Context, principal Principal, req CreateAgentRoleRequest) (CommandResult, error) {
	target := "host/configuration/agent-bindings/role/" + string(agentbinding.NormalizeHandle(req.Role.Handle))
	return s.execute(ctx, principal, ActionAgentRoleCreate, req.WriteBase, target, req)
}

func (s *CommandService) DeleteAgentRole(ctx context.Context, principal Principal, req DeleteAgentRoleRequest) (CommandResult, error) {
	target := "host/configuration/agent-bindings/role/" + string(agentbinding.NormalizeHandle(req.Handle))
	return s.execute(ctx, principal, ActionAgentRoleDelete, req.WriteBase, target, req)
}

func (s *CommandService) SaveAgentBindingSet(ctx context.Context, principal Principal, req AgentBindingSetRequest) (CommandResult, error) {
	target := "host/configuration/agent-bindings/set/" + agentbinding.NormalizeSetName(req.SetName)
	return s.execute(ctx, principal, ActionAgentBindingSetSave, req.WriteBase, target, req)
}

func (s *CommandService) ApplyAgentBindingSet(ctx context.Context, principal Principal, req AgentBindingSetRequest) (CommandResult, error) {
	target := "host/configuration/agent-bindings/set/" + agentbinding.NormalizeSetName(req.SetName)
	return s.execute(ctx, principal, ActionAgentBindingSetApply, req.WriteBase, target, req)
}

func (s *CommandService) DeleteAgentBindingSet(ctx context.Context, principal Principal, req AgentBindingSetRequest) (CommandResult, error) {
	target := "host/configuration/agent-bindings/set/" + agentbinding.NormalizeSetName(req.SetName)
	return s.execute(ctx, principal, ActionAgentBindingSetDelete, req.WriteBase, target, req)
}

func (s *CommandService) DisconnectACP(ctx context.Context, principal Principal, req DisconnectACPRequest) (CommandResult, error) {
	target := "host/configuration/agents/acp/" + controlagents.NormalizeName(req.AgentID)
	return s.execute(ctx, principal, ActionACPAgentDisconnect, req.WriteBase, target, req)
}

func (s *CommandService) PrepareACP(ctx context.Context, principal Principal, req PrepareACPRequest) (CommandResult, error) {
	prepared := controlagents.NormalizeACPPrepareRequest(req.Request)
	adapterID := firstNonEmptyString(prepared.AdapterID, "custom")
	modelID := firstNonEmptyString(prepared.ModelID, "default")
	target := "host/configuration/agents/acp/prepare/" + controlagents.NormalizeName(adapterID) + "/model/" + controlagents.NormalizeName(modelID)
	return s.execute(ctx, principal, ActionACPAgentPrepare, req.WriteBase, target, req)
}

func (s *CommandService) PrepareACPAuthentication(ctx context.Context, principal Principal, req PrepareACPAuthenticationRequest) (CommandResult, error) {
	target := "host/acp-preparations/" + strings.TrimSpace(req.PreparationRef) + "/authentication/" + strings.TrimSpace(req.MethodID)
	return s.execute(ctx, principal, ActionACPAgentPrepareAuth, req.WriteBase, target, req)
}

func (s *CommandService) ConnectACP(ctx context.Context, principal Principal, req ConnectACPRequest) (CommandResult, error) {
	target := "host/configuration/agents/acp/preparation/" + strings.TrimSpace(req.PreparationRef)
	return s.execute(ctx, principal, ActionACPAgentConnect, req.WriteBase, target, req)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func validateAgentBindingCommandRequest(action Action, base WriteBase, handle agentbinding.Handle, setName string) error {
	if strings.TrimSpace(base.SessionID) != "" {
		return errors.New("controlclient: Host Agent binding mutation must not address a Session")
	}
	if base.ExpectedRevision == nil {
		return errors.New("controlclient: Host Agent binding mutation expected_revision is required")
	}
	if strings.TrimSpace(base.ExpectedControllerEpoch) != "" {
		return errors.New("controlclient: Host Agent binding mutation must not address a controller epoch")
	}
	switch action {
	case ActionAgentBindingBind, ActionAgentBindingReset, ActionAgentRoleCreate, ActionAgentRoleDelete:
		if agentbinding.NormalizeHandle(handle) == "" {
			return errors.New("controlclient: Agent binding handle is required")
		}
	case ActionAgentBindingSetSave, ActionAgentBindingSetApply, ActionAgentBindingSetDelete:
		if err := agentbinding.ValidateSetName(setName); err != nil {
			return err
		}
	default:
		return fmt.Errorf("controlclient: unsupported Agent binding action %q", action)
	}
	return nil
}

func validateHostACPWrite(base WriteBase, capability string) error {
	if strings.TrimSpace(base.SessionID) != "" {
		return fmt.Errorf("controlclient: Host ACP %s must not address a Session", capability)
	}
	if base.ExpectedRevision == nil {
		return fmt.Errorf("controlclient: Host ACP %s expected_revision is required", capability)
	}
	if strings.TrimSpace(base.ExpectedControllerEpoch) != "" {
		return fmt.Errorf("controlclient: Host ACP %s must not address a controller epoch", capability)
	}
	return nil
}

func validateACPPrepareCommandRequest(action Action, request PrepareACPRequest) error {
	if action != ActionACPAgentPrepare {
		return fmt.Errorf("controlclient: unsupported ACP prepare action %q", action)
	}
	if err := validateHostACPWrite(request.WriteBase, "prepare"); err != nil {
		return err
	}
	prepared := controlagents.NormalizeACPPrepareRequest(request.Request)
	if prepared.AdapterID == "" && prepared.CommandLine == "" {
		return errors.New("controlclient: ACP prepare requires an adapter or command")
	}
	return nil
}

func validateACPPrepareAuthenticationCommandRequest(action Action, request PrepareACPAuthenticationRequest) error {
	if action != ActionACPAgentPrepareAuth {
		return fmt.Errorf("controlclient: unsupported ACP prepare-auth action %q", action)
	}
	if err := validateHostACPWrite(request.WriteBase, "prepare-auth"); err != nil {
		return err
	}
	if strings.TrimSpace(request.PreparationRef) == "" || strings.TrimSpace(request.PreparationDigest) == "" {
		return errors.New("controlclient: ACP preparation ref and digest are required")
	}
	if strings.TrimSpace(request.MethodID) == "" {
		return errors.New("controlclient: ACP authentication method is required")
	}
	return nil
}

func validateACPConnectCommandRequest(action Action, request ConnectACPRequest) error {
	if action != ActionACPAgentConnect {
		return fmt.Errorf("controlclient: unsupported ACP connect action %q", action)
	}
	if err := validateHostACPWrite(request.WriteBase, "connect"); err != nil {
		return err
	}
	if strings.TrimSpace(request.PreparationRef) == "" || strings.TrimSpace(request.PreparationDigest) == "" {
		return errors.New("controlclient: ready ACP preparation ref and digest are required")
	}
	return nil
}

var _ AgentCommandService = (*CommandService)(nil)
