package gatewayapp

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

func (s *Stack) executeAgentCommand(ctx context.Context, action appserver.Action, request any) (appserver.CommandResult, error) {
	switch typed := request.(type) {
	case appserver.PrepareACPRequest:
		if action != appserver.ActionACPAgentPrepare {
			return appserver.CommandResult{}, configurationRejectedError(
				errorcode.New(errorcode.InvalidArgument, "gatewayapp: ACP prepare request/action mismatch"),
			)
		}
		principalIntent, _ := appserver.OperationIntentFromContext(ctx)
		result, err := s.prepareACPAtRevision(ctx, appserver.Principal{ID: principalIntent.PrincipalID}, typed)
		command := acpPreparationCommandResult(result)
		return command, classifyACPPreparationEffectError(result, err)
	case appserver.PrepareACPAuthenticationRequest:
		if action != appserver.ActionACPAgentPrepareAuth {
			return appserver.CommandResult{}, configurationRejectedError(
				errorcode.New(errorcode.InvalidArgument, "gatewayapp: ACP prepare-auth request/action mismatch"),
			)
		}
		principalIntent, _ := appserver.OperationIntentFromContext(ctx)
		result, err := s.prepareACPAuthenticationAtRevision(ctx, appserver.Principal{ID: principalIntent.PrincipalID}, typed)
		command := acpPreparationCommandResult(result)
		return command, classifyACPPreparationEffectError(result, err)
	case appserver.ConnectACPRequest:
		if action != appserver.ActionACPAgentConnect {
			return appserver.CommandResult{}, configurationRejectedError(
				errorcode.New(errorcode.InvalidArgument, "gatewayapp: ACP connect request/action mismatch"),
			)
		}
		principalIntent, _ := appserver.OperationIntentFromContext(ctx)
		mutation, profile, err := s.connectPreparedACPAtRevision(ctx, appserver.Principal{ID: principalIntent.PrincipalID}, typed)
		command := configurationCommandResult(mutation.Revision)
		if profile.ID != "" {
			command.Resource = &appserver.CommandResource{
				Kind:   appserver.CommandResourceModelProfile,
				Ref:    profile.ID,
				Digest: strings.TrimSpace(typed.PreparationDigest),
			}
		}
		if mutation.Warning != nil {
			command.Detail = "external ACP Agent connected; " + mutation.Warning.Error()
		}
		return command, classifyExternalAgentMutationError(mutation, err)
	case appserver.DisconnectACPRequest:
		if action != appserver.ActionACPAgentDisconnect {
			return appserver.CommandResult{}, configurationRejectedError(
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

func acpPreparationCommandResult(result acpPreparationEffectResult) appserver.CommandResult {
	command := configurationCommandResult(result.Revision)
	if result.Preparation.Ref != "" {
		command.Resource = &appserver.CommandResource{
			Kind:   appserver.CommandResourceACPPreparation,
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
		return appserver.NewOutcomeError(appserver.OutcomeConflicted, coded)
	}
	if result.EffectStarted {
		return appserver.NewOutcomeError(
			appserver.OutcomeUnknown,
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
		return appserver.NewOutcomeError(appserver.OutcomeConflicted, coded)
	}
	if result.EffectStarted {
		return appserver.NewOutcomeError(
			appserver.OutcomeUnknown,
			errorcode.Wrap(errorcode.UnknownOutcome, "gatewayapp: external Agent mutation outcome cannot be proven", err),
		)
	}
	if errorcode.CodeOf(err) == errorcode.Unknown {
		err = errorcode.Wrap(errorcode.FailedPrecondition, err.Error(), err)
	}
	return configurationRejectedError(err)
}
