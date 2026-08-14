package gatewayapp

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	controlclient "github.com/caelis-labs/caelis/control/client"
)

func (s *Stack) executeAgentCommand(ctx context.Context, action controlclient.Action, request any) (controlclient.CommandResult, error) {
	switch typed := request.(type) {
	case controlclient.PrepareACPRequest:
		if action != controlclient.ActionACPAgentPrepare {
			return controlclient.CommandResult{}, configurationRejectedError(
				errorcode.New(errorcode.InvalidArgument, "gatewayapp: ACP prepare request/action mismatch"),
			)
		}
		principalIntent, _ := controlclient.OperationIntentFromContext(ctx)
		result, err := s.prepareACPAtRevision(ctx, controlclient.Principal{ID: principalIntent.PrincipalID}, typed)
		command := acpPreparationCommandResult(result)
		return command, classifyACPPreparationEffectError(result, err)
	case controlclient.PrepareACPAuthenticationRequest:
		if action != controlclient.ActionACPAgentPrepareAuth {
			return controlclient.CommandResult{}, configurationRejectedError(
				errorcode.New(errorcode.InvalidArgument, "gatewayapp: ACP prepare-auth request/action mismatch"),
			)
		}
		principalIntent, _ := controlclient.OperationIntentFromContext(ctx)
		result, err := s.prepareACPAuthenticationAtRevision(ctx, controlclient.Principal{ID: principalIntent.PrincipalID}, typed)
		command := acpPreparationCommandResult(result)
		return command, classifyACPPreparationEffectError(result, err)
	case controlclient.ConnectACPRequest:
		if action != controlclient.ActionACPAgentConnect {
			return controlclient.CommandResult{}, configurationRejectedError(
				errorcode.New(errorcode.InvalidArgument, "gatewayapp: ACP connect request/action mismatch"),
			)
		}
		principalIntent, _ := controlclient.OperationIntentFromContext(ctx)
		mutation, profile, err := s.connectPreparedACPAtRevision(ctx, controlclient.Principal{ID: principalIntent.PrincipalID}, typed)
		command := configurationCommandResult(mutation.Revision)
		if profile.ID != "" {
			command.Resource = &controlclient.CommandResource{
				Kind:   controlclient.CommandResourceModelProfile,
				Ref:    profile.ID,
				Digest: strings.TrimSpace(typed.PreparationDigest),
			}
		}
		if mutation.Warning != nil {
			command.Detail = "external ACP Agent connected; " + mutation.Warning.Error()
		}
		return command, classifyExternalAgentMutationError(mutation, err)
	case controlclient.DisconnectACPRequest:
		if action != controlclient.ActionACPAgentDisconnect {
			return controlclient.CommandResult{}, configurationRejectedError(
				errorcode.New(errorcode.InvalidArgument, "gatewayapp: ACP disconnect request/action mismatch"),
			)
		}
		mutation, _, err := s.disconnectACPAtRevision(
			ctx,
			typed.AgentID,
			expectedConfigurationRevision(typed.ExpectedRevision),
		)
		result := configurationCommandResult(mutation.Revision)
		if mutation.Warning != nil {
			result.Detail = "external ACP Agent disconnected; " + mutation.Warning.Error()
		}
		return result, classifyExternalAgentMutationError(mutation, err)
	default:
		return s.executeAgentBindingCommand(ctx, action, request)
	}
}

func acpPreparationCommandResult(result acpPreparationEffectResult) controlclient.CommandResult {
	command := configurationCommandResult(result.Revision)
	if result.Preparation.Ref != "" {
		command.Resource = &controlclient.CommandResource{
			Kind:   controlclient.CommandResourceACPPreparation,
			Ref:    result.Preparation.Ref,
			Digest: result.Preparation.ContentDigest,
		}
	}
	if result.Warning != nil {
		command.Detail = "ACP preparation committed with a warning: " + result.Warning.Error()
	}
	return command
}

func classifyACPPreparationEffectError(result acpPreparationEffectResult, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, configstore.ErrConfigurationRevisionConflict) {
		coded := errorcode.Wrap(errorcode.Conflict, "gatewayapp: ACP preparation configuration conflict", err)
		return controlclient.NewOutcomeError(controlclient.OutcomeConflicted, coded)
	}
	if result.EffectStarted {
		return controlclient.NewOutcomeError(
			controlclient.OutcomeUnknown,
			errorcode.Wrap(errorcode.UnknownOutcome, "gatewayapp: ACP preparation outcome cannot be proven", err),
		)
	}
	if errorcode.CodeOf(err) == errorcode.Unknown {
		err = errorcode.Wrap(errorcode.FailedPrecondition, err.Error(), err)
	}
	return configurationRejectedError(err)
}

func classifyExternalAgentMutationError(result externalAgentMutationResult, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, configstore.ErrConfigurationRevisionConflict) {
		coded := errorcode.Wrap(errorcode.Conflict, "gatewayapp: external Agent configuration conflict", err)
		return controlclient.NewOutcomeError(controlclient.OutcomeConflicted, coded)
	}
	if result.EffectStarted {
		return controlclient.NewOutcomeError(
			controlclient.OutcomeUnknown,
			errorcode.Wrap(errorcode.UnknownOutcome, "gatewayapp: external Agent mutation outcome cannot be proven", err),
		)
	}
	if errorcode.CodeOf(err) == errorcode.Unknown {
		err = errorcode.Wrap(errorcode.FailedPrecondition, err.Error(), err)
	}
	return configurationRejectedError(err)
}
