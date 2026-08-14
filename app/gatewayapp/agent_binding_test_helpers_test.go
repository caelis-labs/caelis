package gatewayapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/caelis-labs/caelis/control/agentbinding"
	controlclient "github.com/caelis-labs/caelis/control/client"
)

// testAgentBindingService keeps package-level setup concise while exercising
// the same shared command executor and Host CAS as product AppServer clients.
type testAgentBindingService struct{ stack *Stack }

func (s *Stack) testAgentBindings() agentbinding.ConfigurationService {
	return testAgentBindingService{stack: s}
}

func (s testAgentBindingService) AgentBindingStatus(ctx context.Context) (agentbinding.Status, error) {
	return s.stack.AgentBindings().AgentBindingStatus(ctx)
}

func (s testAgentBindingService) BindAgentBinding(ctx context.Context, binding agentbinding.Binding) (agentbinding.Status, error) {
	return s.mutate(ctx, "test-agent-binding", func(base controlclient.WriteBase) (controlclient.CommandResult, error) {
		return s.commands().BindAgentBinding(ctx, s.principal(), controlclient.BindAgentBindingRequest{WriteBase: base, Binding: binding})
	})
}

func (s testAgentBindingService) ResetAgentBinding(ctx context.Context, handle agentbinding.Handle) (agentbinding.Status, error) {
	return s.mutate(ctx, "test-agent-binding-reset", func(base controlclient.WriteBase) (controlclient.CommandResult, error) {
		return s.commands().ResetAgentBinding(ctx, s.principal(), controlclient.ResetAgentBindingRequest{WriteBase: base, Handle: handle})
	})
}

func (s testAgentBindingService) CreateAgentRole(ctx context.Context, role agentbinding.Role, binding agentbinding.Binding) (agentbinding.Status, error) {
	return s.mutate(ctx, "test-agent-role-create", func(base controlclient.WriteBase) (controlclient.CommandResult, error) {
		return s.commands().CreateAgentRole(ctx, s.principal(), controlclient.CreateAgentRoleRequest{WriteBase: base, Role: role, Binding: binding})
	})
}

func (s testAgentBindingService) DeleteAgentRole(ctx context.Context, handle agentbinding.Handle) (agentbinding.Status, error) {
	return s.mutate(ctx, "test-agent-role-delete", func(base controlclient.WriteBase) (controlclient.CommandResult, error) {
		return s.commands().DeleteAgentRole(ctx, s.principal(), controlclient.DeleteAgentRoleRequest{WriteBase: base, Handle: handle})
	})
}

func (s testAgentBindingService) SaveAgentBindingSet(ctx context.Context, name string) (agentbinding.Status, error) {
	return s.bindingSet(ctx, "test-agent-binding-set-save", name, s.commands().SaveAgentBindingSet)
}

func (s testAgentBindingService) ApplyAgentBindingSet(ctx context.Context, name string) (agentbinding.Status, error) {
	return s.bindingSet(ctx, "test-agent-binding-set-apply", name, s.commands().ApplyAgentBindingSet)
}

func (s testAgentBindingService) DeleteAgentBindingSet(ctx context.Context, name string) (agentbinding.Status, error) {
	return s.bindingSet(ctx, "test-agent-binding-set-delete", name, s.commands().DeleteAgentBindingSet)
}

func (s testAgentBindingService) bindingSet(
	ctx context.Context,
	prefix string,
	name string,
	command func(context.Context, controlclient.Principal, controlclient.AgentBindingSetRequest) (controlclient.CommandResult, error),
) (agentbinding.Status, error) {
	return s.mutate(ctx, prefix, func(base controlclient.WriteBase) (controlclient.CommandResult, error) {
		return command(ctx, s.principal(), controlclient.AgentBindingSetRequest{WriteBase: base, SetName: name})
	})
}

func (s testAgentBindingService) mutate(
	ctx context.Context,
	prefix string,
	command func(controlclient.WriteBase) (controlclient.CommandResult, error),
) (agentbinding.Status, error) {
	revision, err := s.stack.ConfigurationRevision(ctx)
	if err != nil {
		return agentbinding.Status{}, err
	}
	result, commandErr := command(controlclient.WriteBase{
		OperationID:      prefix + "-" + uuid.NewString(),
		ExpectedRevision: &revision,
	})
	if result.Outcome != controlclient.OutcomeCommitted {
		return agentbinding.Status{}, errors.Join(commandErr, fmt.Errorf("Agent binding test command outcome is %q", result.Outcome))
	}
	status, observationErr := s.stack.AgentBindings().AgentBindingStatus(ctx)
	return status, errors.Join(commandErr, observationErr)
}

func (s testAgentBindingService) principal() controlclient.Principal {
	return controlclient.Principal{ID: firstNonEmpty(s.stack.UserID, "gatewayapp-test")}
}

func (s testAgentBindingService) commands() controlclient.AgentCommandService {
	if commands := s.stack.AgentCommands(); commands != nil {
		return commands
	}
	commands, err := controlclient.NewCommandService(controlclient.CommandServiceConfig{
		Authorizer: controlclient.ProductCommandAuthorizer{},
		Operations: controlclient.NewMemoryOperationStore(),
		Backend:    s.stack,
	})
	if err != nil {
		panic(err)
	}
	s.stack.agentCommands = commands
	return commands
}

var _ agentbinding.ConfigurationService = testAgentBindingService{}
