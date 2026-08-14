package gatewayapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	controlclient "github.com/caelis-labs/caelis/control/client"
)

func (s *Stack) executeConfigurationCommand(ctx context.Context, action controlclient.Action, request any) (controlclient.CommandResult, error) {
	switch request.(type) {
	case controlclient.ConnectModelRequest, controlclient.UseModelRequest, controlclient.DeleteModelRequest:
		return s.executeHostModelConfigurationCommand(ctx, action, request)
	}
	req, ok := request.(controlclient.SandboxRequest)
	if !ok {
		return controlclient.CommandResult{}, configurationRejectedError(
			errorcode.New(errorcode.InvalidArgument, "gatewayapp: invalid configuration command request"),
		)
	}
	var revision uint64
	var effectStarted bool
	var err error
	switch action {
	case controlclient.ActionSandboxBackend:
		_, revision, err = s.setSandboxBackendAtRevision(ctx, req.Backend, req.ExpectedRevision)
	case controlclient.ActionSandboxPrepare:
		_, revision, effectStarted, err = s.runSandboxLifecycleCommand(ctx, sandboxLifecycleCommand{
			action: prepareSandboxRuntime,
		}, req.ExpectedRevision)
	case controlclient.ActionSandboxRepair:
		_, revision, effectStarted, err = s.runSandboxLifecycleCommand(ctx, sandboxLifecycleCommand{
			action: repairSandboxRuntime,
		}, req.ExpectedRevision)
	case controlclient.ActionSandboxReset:
		_, revision, effectStarted, err = s.runSandboxLifecycleCommand(ctx, sandboxLifecycleCommand{
			action: resetSandboxRuntime,
		}, req.ExpectedRevision)
	case controlclient.ActionSandboxRefresh:
		_, revision, effectStarted, err = s.runSandboxLifecycleCommand(ctx, sandboxLifecycleCommand{
			refresh: true,
		}, req.ExpectedRevision)
	default:
		return configurationCommandResult(revision), configurationRejectedError(
			errorcode.New(errorcode.InvalidArgument, fmt.Sprintf("gatewayapp: unsupported sandbox command %q", action)),
		)
	}
	if action != controlclient.ActionSandboxBackend && !effectStarted {
		return configurationCommandResult(revision), classifyConfigurationPreEffectError(err)
	}
	return configurationCommandResult(revision), classifyConfigurationEffectError(err)
}

func configurationCommandResult(revision uint64) controlclient.CommandResult {
	return controlclient.CommandResult{Outcome: controlclient.OutcomeCommitted, Revision: revision}
}

func classifyConfigurationPreEffectError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, configstore.ErrConfigurationRevisionConflict) {
		coded := errorcode.Wrap(errorcode.Conflict, "gatewayapp: configuration conflict", err)
		return controlclient.NewOutcomeError(controlclient.OutcomeConflicted, coded)
	}
	if errorcode.CodeOf(err) == errorcode.Unknown {
		err = errorcode.Wrap(errorcode.Unavailable, "gatewayapp: read configuration revision", err)
	}
	return configurationRejectedError(err)
}

func classifyConfigurationEffectError(err error) error {
	if err == nil {
		return nil
	}
	var outcomeErr *controlclient.OutcomeError
	if errors.As(err, &outcomeErr) {
		return err
	}
	return controlclient.NewOutcomeError(controlclient.OutcomeUnknown, err)
}

func configurationRejectedError(err error) error {
	return controlclient.NewOutcomeError(controlclient.OutcomeRejected, err)
}
