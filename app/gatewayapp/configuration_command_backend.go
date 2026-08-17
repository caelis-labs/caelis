package gatewayapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

func (s *controlCommandBackend) executeConfigurationCommand(ctx context.Context, action appserver.Action, request any) (appserver.CommandResult, error) {
	switch request.(type) {
	case appserver.ConnectModelRequest, appserver.UseModelRequest, appserver.DeleteModelRequest:
		return s.executeHostModelConfigurationCommand(ctx, action, request)
	}
	req, ok := request.(appserver.SandboxRequest)
	if !ok {
		return appserver.CommandResult{}, configurationRejectedError(
			errorcode.New(errorcode.InvalidArgument, "gatewayapp: invalid configuration command request"),
		)
	}
	var revision uint64
	var effectStarted bool
	var err error
	switch action {
	case appserver.ActionSandboxBackend:
		_, revision, err = s.setSandboxBackendAtRevision(ctx, req.Backend, req.ExpectedRevision)
	case appserver.ActionSandboxPrepare:
		_, revision, effectStarted, err = s.runSandboxLifecycleCommand(ctx, sandboxLifecycleCommand{
			action: prepareSandboxRuntime,
		}, req.ExpectedRevision)
	case appserver.ActionSandboxRepair:
		_, revision, effectStarted, err = s.runSandboxLifecycleCommand(ctx, sandboxLifecycleCommand{
			action: repairSandboxRuntime,
		}, req.ExpectedRevision)
	case appserver.ActionSandboxReset:
		_, revision, effectStarted, err = s.runSandboxLifecycleCommand(ctx, sandboxLifecycleCommand{
			action: resetSandboxRuntime,
		}, req.ExpectedRevision)
	case appserver.ActionSandboxRefresh:
		_, revision, effectStarted, err = s.runSandboxLifecycleCommand(ctx, sandboxLifecycleCommand{
			refresh: true,
		}, req.ExpectedRevision)
	default:
		return configurationCommandResult(revision), configurationRejectedError(
			errorcode.New(errorcode.InvalidArgument, fmt.Sprintf("gatewayapp: unsupported sandbox command %q", action)),
		)
	}
	if action != appserver.ActionSandboxBackend && !effectStarted {
		return configurationCommandResult(revision), classifyConfigurationPreEffectError(err)
	}
	return configurationCommandResult(revision), classifyConfigurationEffectError(err)
}

func configurationCommandResult(revision uint64) appserver.CommandResult {
	return appserver.CommandResult{Outcome: appserver.OutcomeCommitted, Revision: revision}
}

func classifyConfigurationPreEffectError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, configstore.ErrConfigurationRevisionConflict) {
		coded := errorcode.Wrap(errorcode.Conflict, "gatewayapp: configuration conflict", err)
		return appserver.NewOutcomeError(appserver.OutcomeConflicted, coded)
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
	var outcomeErr *appserver.OutcomeError
	if errors.As(err, &outcomeErr) {
		return err
	}
	return appserver.NewOutcomeError(appserver.OutcomeUnknown, err)
}

func configurationRejectedError(err error) error {
	return appserver.NewOutcomeError(appserver.OutcomeRejected, err)
}
