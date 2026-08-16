package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/plugin"
)

type pluginMutationResult struct {
	Revision      uint64
	EffectStarted bool
	Warning       error
	ResourceKind  string
	ResourceRef   string
}

func isHostPluginCommandRequest(request any) bool {
	switch request.(type) {
	case appserver.AddMarketplaceRequest,
		appserver.UpdateMarketplaceRequest,
		appserver.RemoveMarketplaceRequest,
		appserver.AddPluginPathRequest,
		appserver.InstallPluginRequest,
		appserver.EnablePluginRequest,
		appserver.DisablePluginRequest,
		appserver.RemovePluginRequest:
		return true
	default:
		return false
	}
}

func recoverablePluginCommandAction(action appserver.Action) bool {
	switch action {
	case appserver.ActionPluginMarketplaceAdd,
		appserver.ActionPluginMarketplaceUpdate,
		appserver.ActionPluginInstall:
		return true
	default:
		return false
	}
}

func (s *Stack) executePluginCommand(ctx context.Context, action appserver.Action, request any) (appserver.CommandResult, error) {
	result, err := s.mutatePluginAtRevision(ctx, action, request)
	command := configurationCommandResult(result.Revision)
	if result.ResourceRef != "" {
		command.Resource = &appserver.CommandResource{
			Kind: firstNonEmpty(result.ResourceKind, appserver.CommandResourcePlugin),
			Ref:  result.ResourceRef,
		}
	}
	if result.Warning != nil {
		command.Detail = "plugin configuration committed; " + result.Warning.Error()
	}
	if err == nil && recoverablePluginCommandAction(action) {
		if receiptErr := s.persistPluginCommandReceipt(ctx, action, command); receiptErr != nil {
			return command, appserver.NewOutcomeError(
				appserver.OutcomeUnknown,
				errorcode.Wrap(errorcode.UnknownOutcome, "gatewayapp: persist plugin operation receipt", receiptErr),
			)
		}
	}
	return command, classifyPluginMutationError(result, err)
}

func (s *Stack) mutatePluginAtRevision(ctx context.Context, action appserver.Action, request any) (pluginMutationResult, error) {
	result := pluginMutationResult{}
	if s == nil {
		return result, errors.New("gatewayapp: plugin configuration is unavailable")
	}
	expected, invoke, err := pluginMutationInvoker(action, request)
	if err != nil {
		return result, err
	}
	result.Revision = expected
	ctx = contextOrBackground(ctx)
	// Preflight CAS revision before any external install/update effect so a
	// stale request never mutates managed cache.
	if err := s.preflightPluginConfigurationRevision(ctx, expected); err != nil {
		result.Revision = configurationErrorRevision(err, expected)
		return result, err
	}
	ctx = plugin.WithExpectedRevision(ctx, expected)

	resourceKind, resourceRef, effect, warning, invokeErr := invoke(ctx, s.Plugins())
	result.ResourceKind = resourceKind
	result.ResourceRef = resourceRef
	// EffectStarted is monotonic: once true it is never cleared, including after
	// a later configuration CAS conflict.
	result.EffectStarted = effect.Started
	if invokeErr != nil {
		result.Revision = configurationErrorRevision(invokeErr, result.Revision)
		if configstore.WriteCommitted(invokeErr) {
			result.EffectStarted = true
		}
		return result, invokeErr
	}
	if !result.EffectStarted {
		// Pure AppConfig mutations mark durability after a successful CAS path.
		result.EffectStarted = true
	}
	if revision, ok := s.currentPluginConfigurationRevision(ctx); ok {
		result.Revision = revision
	}
	result.Warning = warning
	return result, nil
}

func (s *Stack) preflightPluginConfigurationRevision(ctx context.Context, expected uint64) error {
	if s == nil || s.store == nil {
		return errors.New("gatewayapp: plugin configuration is unavailable")
	}
	doc, err := s.store.LoadContext(contextOrBackground(ctx))
	if err != nil {
		return err
	}
	if doc.ConfigurationRevision != expected {
		return &configstore.ConfigurationRevisionConflict{
			Expected: expected,
			Actual:   doc.ConfigurationRevision,
		}
	}
	return nil
}

func pluginMutationInvoker(
	action appserver.Action,
	request any,
) (
	expected uint64,
	invoke func(context.Context, PluginService) (resourceKind, resourceRef string, effect plugin.EffectReport, warning error, err error),
	err error,
) {
	switch req := request.(type) {
	case appserver.AddMarketplaceRequest:
		if action != appserver.ActionPluginMarketplaceAdd {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(ctx context.Context, service PluginService) (string, string, plugin.EffectReport, error, error) {
			info, effect, invokeErr := service.AddMarketplace(ctx, req.Source)
			return appserver.CommandResourceMarketplace, info.Name, effect, nil, invokeErr
		}, nil
	case appserver.UpdateMarketplaceRequest:
		if action != appserver.ActionPluginMarketplaceUpdate {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(ctx context.Context, service PluginService) (string, string, plugin.EffectReport, error, error) {
			info, effect, invokeErr := service.UpdateMarketplace(ctx, req.Name)
			return appserver.CommandResourceMarketplace, info.Name, effect, nil, invokeErr
		}, nil
	case appserver.RemoveMarketplaceRequest:
		if action != appserver.ActionPluginMarketplaceRemove {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(ctx context.Context, service PluginService) (string, string, plugin.EffectReport, error, error) {
			return appserver.CommandResourceMarketplace, strings.TrimSpace(req.Name), plugin.EffectReport{}, nil, service.RemoveMarketplace(ctx, req.Name)
		}, nil
	case appserver.AddPluginPathRequest:
		if action != appserver.ActionPluginAddPath {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(ctx context.Context, service PluginService) (string, string, plugin.EffectReport, error, error) {
			info, invokeErr := service.AddPath(ctx, req.Path)
			return appserver.CommandResourcePlugin, info.ID, plugin.EffectReport{}, pluginInfoWarning(info), invokeErr
		}, nil
	case appserver.InstallPluginRequest:
		if action != appserver.ActionPluginInstall {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(ctx context.Context, service PluginService) (string, string, plugin.EffectReport, error, error) {
			info, effect, invokeErr := service.Install(ctx, req.Source)
			return appserver.CommandResourcePlugin, info.ID, effect, pluginInfoWarning(info), invokeErr
		}, nil
	case appserver.EnablePluginRequest:
		if action != appserver.ActionPluginEnable {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(ctx context.Context, service PluginService) (string, string, plugin.EffectReport, error, error) {
			info, invokeErr := service.Enable(ctx, req.ID)
			return appserver.CommandResourcePlugin, info.ID, plugin.EffectReport{}, pluginInfoWarning(info), invokeErr
		}, nil
	case appserver.DisablePluginRequest:
		if action != appserver.ActionPluginDisable {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(ctx context.Context, service PluginService) (string, string, plugin.EffectReport, error, error) {
			info, invokeErr := service.Disable(ctx, req.ID)
			return appserver.CommandResourcePlugin, info.ID, plugin.EffectReport{}, pluginInfoWarning(info), invokeErr
		}, nil
	case appserver.RemovePluginRequest:
		if action != appserver.ActionPluginRemove {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(ctx context.Context, service PluginService) (string, string, plugin.EffectReport, error, error) {
			return appserver.CommandResourcePlugin, strings.TrimSpace(req.ID), plugin.EffectReport{}, nil, service.Remove(ctx, req.ID)
		}, nil
	}
	return 0, nil, errorcode.New(errorcode.InvalidArgument, fmt.Sprintf("gatewayapp: plugin request/action mismatch for %q", action))
}

func pluginInfoWarning(info PluginInfo) error {
	if strings.TrimSpace(info.Warning) == "" {
		return nil
	}
	return errors.New(strings.TrimSpace(info.Warning))
}

func (s *Stack) currentPluginConfigurationRevision(ctx context.Context) (uint64, bool) {
	if s == nil || s.store == nil {
		return 0, false
	}
	doc, err := s.store.LoadContext(contextOrBackground(ctx))
	if err != nil {
		return 0, false
	}
	return doc.ConfigurationRevision, true
}

func (s *Stack) persistPluginCommandReceipt(ctx context.Context, action appserver.Action, command appserver.CommandResult) error {
	intent, ok := appserver.OperationIntentFromContext(ctx)
	if !ok || !recoverablePluginCommandAction(action) {
		return nil
	}
	kind := ""
	target := ""
	if command.Resource != nil {
		kind = command.Resource.Kind
		target = command.Resource.Ref
	}
	if kind == "" {
		kind = resourceKindForPluginAction(action)
	}
	return s.writePluginOperationReceipt(ctx, pluginOperationReceipt{
		PrincipalID:  intent.PrincipalID,
		OperationID:  intent.OperationID,
		Digest:       intent.Digest,
		Action:       action,
		Outcome:      command.Outcome,
		Revision:     command.Revision,
		Detail:       command.Detail,
		ResourceKind: kind,
		Target:       target,
	})
}

func resourceKindForPluginAction(action appserver.Action) string {
	switch action {
	case appserver.ActionPluginMarketplaceAdd, appserver.ActionPluginMarketplaceUpdate, appserver.ActionPluginMarketplaceRemove:
		return appserver.CommandResourceMarketplace
	default:
		return appserver.CommandResourcePlugin
	}
}

func classifyPluginMutationError(result pluginMutationResult, err error) error {
	if err == nil {
		return nil
	}
	// Only pre-effect CAS conflicts are safe "no effect" conflicts.
	if !result.EffectStarted && errors.Is(err, configstore.ErrConfigurationRevisionConflict) {
		coded := errorcode.Wrap(errorcode.Conflict, "gatewayapp: plugin configuration conflict", err)
		return appserver.NewOutcomeError(appserver.OutcomeConflicted, coded)
	}
	if result.EffectStarted {
		return appserver.NewOutcomeError(
			appserver.OutcomeUnknown,
			errorcode.Wrap(errorcode.UnknownOutcome, "gatewayapp: plugin mutation outcome cannot be proven", err),
		)
	}
	if errorcode.CodeOf(err) == errorcode.Unknown {
		err = errorcode.Wrap(errorcode.FailedPrecondition, err.Error(), err)
	}
	return configurationRejectedError(err)
}
