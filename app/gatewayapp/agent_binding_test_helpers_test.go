package gatewayapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/caelis-labs/caelis/control/agentbinding"
	appserver "github.com/caelis-labs/caelis/control/appserver"
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
	return s.mutate(ctx, "test-agent-binding", func(base appserver.WriteBase) (appserver.CommandResult, error) {
		return s.commands().BindAgentBinding(ctx, s.principal(), appserver.BindAgentBindingRequest{WriteBase: base, Binding: binding})
	})
}

func (s testAgentBindingService) ResetAgentBinding(ctx context.Context, handle agentbinding.Handle) (agentbinding.Status, error) {
	return s.mutate(ctx, "test-agent-binding-reset", func(base appserver.WriteBase) (appserver.CommandResult, error) {
		return s.commands().ResetAgentBinding(ctx, s.principal(), appserver.ResetAgentBindingRequest{WriteBase: base, Handle: handle})
	})
}

func (s testAgentBindingService) CreateAgentRole(ctx context.Context, role agentbinding.Role, binding agentbinding.Binding) (agentbinding.Status, error) {
	return s.mutate(ctx, "test-agent-role-create", func(base appserver.WriteBase) (appserver.CommandResult, error) {
		return s.commands().CreateAgentRole(ctx, s.principal(), appserver.CreateAgentRoleRequest{WriteBase: base, Role: role, Binding: binding})
	})
}

func (s testAgentBindingService) DeleteAgentRole(ctx context.Context, handle agentbinding.Handle) (agentbinding.Status, error) {
	return s.mutate(ctx, "test-agent-role-delete", func(base appserver.WriteBase) (appserver.CommandResult, error) {
		return s.commands().DeleteAgentRole(ctx, s.principal(), appserver.DeleteAgentRoleRequest{WriteBase: base, Handle: handle})
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
	command func(context.Context, appserver.Principal, appserver.AgentBindingSetRequest) (appserver.CommandResult, error),
) (agentbinding.Status, error) {
	return s.mutate(ctx, prefix, func(base appserver.WriteBase) (appserver.CommandResult, error) {
		return command(ctx, s.principal(), appserver.AgentBindingSetRequest{WriteBase: base, SetName: name})
	})
}

func (s testAgentBindingService) mutate(
	ctx context.Context,
	prefix string,
	command func(appserver.WriteBase) (appserver.CommandResult, error),
) (agentbinding.Status, error) {
	revision, err := s.stack.ConfigurationRevision(ctx)
	if err != nil {
		return agentbinding.Status{}, err
	}
	result, commandErr := command(appserver.WriteBase{
		OperationID:      prefix + "-" + uuid.NewString(),
		ExpectedRevision: &revision,
	})
	if result.Outcome != appserver.OutcomeCommitted {
		return agentbinding.Status{}, errors.Join(commandErr, fmt.Errorf("Agent binding test command outcome is %q", result.Outcome))
	}
	status, observationErr := s.stack.AgentBindings().AgentBindingStatus(ctx)
	return status, errors.Join(commandErr, observationErr)
}

func (s testAgentBindingService) principal() appserver.Principal {
	return appserver.Principal{ID: firstNonEmpty(s.stack.composition.authorities.userID, "gatewayapp-test")}
}

func (s testAgentBindingService) commands() appserver.AgentCommandService {
	if commands := s.stack.AgentCommands(); commands != nil {
		return commands
	}
	commands, err := appserver.NewCommandService(appserver.CommandServiceConfig{
		Authorizer: appserver.ProductCommandAuthorizer{},
		Operations: appserver.NewMemoryOperationStore(),
		Backend:    s.stack,
	})
	if err != nil {
		panic(err)
	}
	s.stack.agentCommands = commands
	return commands
}

var _ agentbinding.ConfigurationService = testAgentBindingService{}
