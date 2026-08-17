package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/agentbinding"
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

type agentBindingMutationResult struct {
	Revision      uint64
	EffectStarted bool
	Warning       error
}

func (s *Stack) executeAgentBindingCommand(ctx context.Context, action appserver.Action, request any) (appserver.CommandResult, error) {
	result, err := s.mutateAgentBindingsAtRevision(ctx, action, request)
	commandResult := configurationCommandResult(result.Revision)
	if result.Warning != nil {
		commandResult.Detail = "Agent binding configuration committed; " + result.Warning.Error()
	}
	return commandResult, classifyAgentBindingMutationError(result, err)
}

func (s *Stack) mutateAgentBindingsAtRevision(ctx context.Context, action appserver.Action, request any) (agentBindingMutationResult, error) {
	result := agentBindingMutationResult{}
	expected, update, err := agentBindingMutation(action, request)
	if err != nil {
		return result, err
	}
	if s == nil || s.composition.authorities.store == nil {
		return result, errors.New("gatewayapp: Agent binding configuration is unavailable")
	}
	ctx = contextOrBackground(ctx)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	doc, err := s.composition.authorities.store.LoadContext(ctx)
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
	if err := s.composition.validateAgentAssemblyCandidate(doc); err != nil {
		return result, errorcode.Wrap(errorcode.InvalidArgument, "gatewayapp: invalid Agent binding assembly", err)
	}
	doc.AgentBindings = agentbinding.NormalizeConfiguration(next)

	saved, persistErr := s.composition.authorities.store.CompareAndSave(ctx, expected, doc)
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
	action appserver.Action,
	request any,
) (expected uint64, update func(AppConfig) (agentbinding.Configuration, error), err error) {
	switch req := request.(type) {
	case appserver.BindAgentBindingRequest:
		if action != appserver.ActionAgentBindingBind {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(doc AppConfig) (agentbinding.Configuration, error) {
			return agentbinding.Bind(doc.AgentBindings, req.Binding, doc.ModelProfiles)
		}, nil
	case appserver.ResetAgentBindingRequest:
		if action != appserver.ActionAgentBindingReset {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(doc AppConfig) (agentbinding.Configuration, error) {
			return agentbinding.Reset(doc.AgentBindings, req.Handle)
		}, nil
	case appserver.CreateAgentRoleRequest:
		if action != appserver.ActionAgentRoleCreate {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(doc AppConfig) (agentbinding.Configuration, error) {
			return agentbinding.CreateRole(doc.AgentBindings, req.Role, req.Binding, doc.ModelProfiles)
		}, nil
	case appserver.DeleteAgentRoleRequest:
		if action != appserver.ActionAgentRoleDelete {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(doc AppConfig) (agentbinding.Configuration, error) {
			return agentbinding.DeleteRole(doc.AgentBindings, req.Handle)
		}, nil
	case appserver.AgentBindingSetRequest:
		switch action {
		case appserver.ActionAgentBindingSetSave:
			return expectedConfigurationRevision(req.ExpectedRevision), func(doc AppConfig) (agentbinding.Configuration, error) {
				return agentbinding.SaveBindingSet(doc.AgentBindings, req.SetName)
			}, nil
		case appserver.ActionAgentBindingSetApply:
			return expectedConfigurationRevision(req.ExpectedRevision), func(doc AppConfig) (agentbinding.Configuration, error) {
				return agentbinding.ApplyBindingSet(doc.AgentBindings, req.SetName, doc.ModelProfiles)
			}, nil
		case appserver.ActionAgentBindingSetDelete:
			return expectedConfigurationRevision(req.ExpectedRevision), func(doc AppConfig) (agentbinding.Configuration, error) {
				return agentbinding.DeleteBindingSet(doc.AgentBindings, req.SetName)
			}, nil
		}
	}
	return 0, nil, errorcode.New(errorcode.InvalidArgument, fmt.Sprintf("gatewayapp: Agent binding request/action mismatch for %q", action))
}

func (s *Stack) reconcileCommittedAgentBindings(ctx context.Context) error {
	if s == nil || s.composition.authorities.store == nil {
		return errors.New("gatewayapp: Agent binding configuration is unavailable")
	}
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(contextOrBackground(ctx)), 5*time.Second)
	defer cancel()
	_, err := s.composition.authorities.store.LoadContext(reconcileCtx)
	return err
}

func classifyAgentBindingMutationError(result agentBindingMutationResult, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, configstore.ErrConfigurationRevisionConflict) {
		coded := errorcode.Wrap(errorcode.Conflict, "gatewayapp: Agent binding configuration conflict", err)
		return appserver.NewOutcomeError(appserver.OutcomeConflicted, coded)
	}
	if result.EffectStarted {
		return appserver.NewOutcomeError(appserver.OutcomeUnknown, err)
	}
	if errorcode.CodeOf(err) == errorcode.Unknown {
		err = errorcode.Wrap(errorcode.Unavailable, "gatewayapp: mutate Agent binding configuration", err)
	}
	return appserver.NewOutcomeError(appserver.OutcomeRejected, err)
}
