package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlclient "github.com/caelis-labs/caelis/control/client"
)

type agentBindingMutationResult struct {
	Revision      uint64
	EffectStarted bool
	Warning       error
}

func (s *Stack) executeAgentBindingCommand(ctx context.Context, action controlclient.Action, request any) (controlclient.CommandResult, error) {
	result, err := s.mutateAgentBindingsAtRevision(ctx, action, request)
	commandResult := configurationCommandResult(result.Revision)
	if result.Warning != nil {
		commandResult.Detail = "Agent binding configuration committed; " + result.Warning.Error()
	}
	return commandResult, classifyAgentBindingMutationError(result, err)
}

func (s *Stack) mutateAgentBindingsAtRevision(ctx context.Context, action controlclient.Action, request any) (agentBindingMutationResult, error) {
	result := agentBindingMutationResult{}
	expected, update, err := agentBindingMutation(action, request)
	if err != nil {
		return result, err
	}
	if s == nil || s.store == nil {
		return result, errors.New("gatewayapp: Agent binding configuration is unavailable")
	}
	ctx = contextOrBackground(ctx)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	doc, err := s.store.LoadContext(ctx)
	if err != nil {
		return result, err
	}
	result.Revision = doc.ConfigurationRevision
	if doc.ConfigurationRevision != expected {
		return result, &configstore.ConfigurationRevisionConflict{Expected: expected, Actual: doc.ConfigurationRevision}
	}
	next, err := update(doc)
	if err != nil {
		return result, errorcode.Wrap(errorcode.InvalidArgument, "gatewayapp: invalid Agent binding mutation", err)
	}
	doc.AgentBindings = next
	if err := s.validateAgentAssemblyCandidate(doc); err != nil {
		return result, errorcode.Wrap(errorcode.InvalidArgument, "gatewayapp: invalid Agent binding assembly", err)
	}
	doc.AgentBindings = agentbinding.NormalizeConfiguration(next)

	saved, persistErr := s.store.CompareAndSave(ctx, expected, doc)
	if persistErr != nil && !configstore.WriteCommitted(persistErr) {
		result.Revision = configurationErrorRevision(persistErr, saved.ConfigurationRevision)
		return result, persistErr
	}
	result.EffectStarted = true
	result.Revision = configurationErrorRevision(persistErr, saved.ConfigurationRevision)
	if saved.ConfigurationRevision == 0 {
		reconcileErr := s.reconcileCommittedAgentBindings(ctx)
		return result, errors.Join(
			persistErr,
			errors.New("gatewayapp: committed Agent binding configuration revision is unknown"),
			wrapOptionalError("gatewayapp: reconcile unobserved Agent binding configuration", reconcileErr),
		)
	}
	result.Warning = wrapOptionalError("gatewayapp: Agent binding configuration durability warning", persistErr)
	return result, nil
}

func agentBindingMutation(
	action controlclient.Action,
	request any,
) (expected uint64, update func(AppConfig) (agentbinding.Configuration, error), err error) {
	switch req := request.(type) {
	case controlclient.BindAgentBindingRequest:
		if action != controlclient.ActionAgentBindingBind {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(doc AppConfig) (agentbinding.Configuration, error) {
			return agentbinding.Bind(doc.AgentBindings, req.Binding, doc.ModelProfiles)
		}, nil
	case controlclient.ResetAgentBindingRequest:
		if action != controlclient.ActionAgentBindingReset {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(doc AppConfig) (agentbinding.Configuration, error) {
			return agentbinding.Reset(doc.AgentBindings, req.Handle)
		}, nil
	case controlclient.CreateAgentRoleRequest:
		if action != controlclient.ActionAgentRoleCreate {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(doc AppConfig) (agentbinding.Configuration, error) {
			return agentbinding.CreateRole(doc.AgentBindings, req.Role, req.Binding, doc.ModelProfiles)
		}, nil
	case controlclient.DeleteAgentRoleRequest:
		if action != controlclient.ActionAgentRoleDelete {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(doc AppConfig) (agentbinding.Configuration, error) {
			return agentbinding.DeleteRole(doc.AgentBindings, req.Handle)
		}, nil
	case controlclient.AgentBindingSetRequest:
		switch action {
		case controlclient.ActionAgentBindingSetSave:
			return expectedConfigurationRevision(req.ExpectedRevision), func(doc AppConfig) (agentbinding.Configuration, error) {
				return agentbinding.SaveBindingSet(doc.AgentBindings, req.SetName)
			}, nil
		case controlclient.ActionAgentBindingSetApply:
			return expectedConfigurationRevision(req.ExpectedRevision), func(doc AppConfig) (agentbinding.Configuration, error) {
				return agentbinding.ApplyBindingSet(doc.AgentBindings, req.SetName, doc.ModelProfiles)
			}, nil
		case controlclient.ActionAgentBindingSetDelete:
			return expectedConfigurationRevision(req.ExpectedRevision), func(doc AppConfig) (agentbinding.Configuration, error) {
				return agentbinding.DeleteBindingSet(doc.AgentBindings, req.SetName)
			}, nil
		}
	}
	return 0, nil, errorcode.New(errorcode.InvalidArgument, fmt.Sprintf("gatewayapp: Agent binding request/action mismatch for %q", action))
}

func (s *Stack) reconcileCommittedAgentBindings(ctx context.Context) error {
	if s == nil || s.store == nil {
		return errors.New("gatewayapp: Agent binding configuration is unavailable")
	}
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(contextOrBackground(ctx)), 5*time.Second)
	defer cancel()
	_, err := s.store.LoadContext(reconcileCtx)
	return err
}

func classifyAgentBindingMutationError(result agentBindingMutationResult, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, configstore.ErrConfigurationRevisionConflict) {
		coded := errorcode.Wrap(errorcode.Conflict, "gatewayapp: Agent binding configuration conflict", err)
		return controlclient.NewOutcomeError(controlclient.OutcomeConflicted, coded)
	}
	if result.EffectStarted {
		return controlclient.NewOutcomeError(controlclient.OutcomeUnknown, err)
	}
	if errorcode.CodeOf(err) == errorcode.Unknown {
		err = errorcode.Wrap(errorcode.Unavailable, "gatewayapp: mutate Agent binding configuration", err)
	}
	return controlclient.NewOutcomeError(controlclient.OutcomeRejected, err)
}
