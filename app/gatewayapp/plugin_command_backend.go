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
	Revision     uint64
	Warning      error
	ResourceKind string
	ResourceRef  string
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

func (s *controlCommandBackend) executePluginCommand(ctx context.Context, action appserver.Action, request any) (appserver.CommandResult, error) {
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
	return command, classifyPluginMutationError(err)
}

func (s *controlCommandBackend) mutatePluginAtRevision(ctx context.Context, action appserver.Action, request any) (pluginMutationResult, error) {
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

	resourceKind, resourceRef, warning, invokeErr := invoke(ctx, s.plugins())
	result.ResourceKind = resourceKind
	result.ResourceRef = resourceRef
	if invokeErr != nil {
		if configstore.WriteCommitted(invokeErr) {
			if revision, ok := s.currentPluginConfigurationRevision(ctx); ok {
				result.Revision = revision
				result.Warning = errors.Join(warning, fmt.Errorf("plugin configuration durability warning: %w", invokeErr))
				return result, nil
			}
			result.Revision = 0
		}
		result.Revision = configurationErrorRevision(invokeErr, result.Revision)
		return result, invokeErr
	}
	if revision, ok := s.currentPluginConfigurationRevision(ctx); ok {
		result.Revision = revision
	}
	result.Warning = warning
	return result, nil
}

func (s *controlCommandBackend) preflightPluginConfigurationRevision(ctx context.Context, expected uint64) error {
	if s == nil || s.composition.authorities.store == nil {
		return errors.New("gatewayapp: plugin configuration is unavailable")
	}
	doc, err := s.composition.authorities.store.LoadContext(contextOrBackground(ctx))
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
	invoke func(context.Context, PluginService) (resourceKind, resourceRef string, warning error, err error),
	err error,
) {
	switch req := request.(type) {
	case appserver.AddMarketplaceRequest:
		if action != appserver.ActionPluginMarketplaceAdd {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(ctx context.Context, service PluginService) (string, string, error, error) {
			info, invokeErr := service.AddMarketplace(ctx, req.Source)
			return appserver.CommandResourceMarketplace, info.Name, nil, invokeErr
		}, nil
	case appserver.UpdateMarketplaceRequest:
		if action != appserver.ActionPluginMarketplaceUpdate {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(ctx context.Context, service PluginService) (string, string, error, error) {
			info, invokeErr := service.UpdateMarketplace(ctx, req.Name)
			return appserver.CommandResourceMarketplace, info.Name, nil, invokeErr
		}, nil
	case appserver.RemoveMarketplaceRequest:
		if action != appserver.ActionPluginMarketplaceRemove {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(ctx context.Context, service PluginService) (string, string, error, error) {
			return appserver.CommandResourceMarketplace, strings.TrimSpace(req.Name), nil, service.RemoveMarketplace(ctx, req.Name)
		}, nil
	case appserver.AddPluginPathRequest:
		if action != appserver.ActionPluginAddPath {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(ctx context.Context, service PluginService) (string, string, error, error) {
			info, invokeErr := service.AddPath(ctx, req.Path)
			return appserver.CommandResourcePlugin, info.ID, pluginInfoWarning(info), invokeErr
		}, nil
	case appserver.InstallPluginRequest:
		if action != appserver.ActionPluginInstall {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(ctx context.Context, service PluginService) (string, string, error, error) {
			info, invokeErr := service.Install(ctx, req.Source)
			return appserver.CommandResourcePlugin, info.ID, pluginInfoWarning(info), invokeErr
		}, nil
	case appserver.EnablePluginRequest:
		if action != appserver.ActionPluginEnable {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(ctx context.Context, service PluginService) (string, string, error, error) {
			info, invokeErr := service.Enable(ctx, req.ID)
			return appserver.CommandResourcePlugin, info.ID, pluginInfoWarning(info), invokeErr
		}, nil
	case appserver.DisablePluginRequest:
		if action != appserver.ActionPluginDisable {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(ctx context.Context, service PluginService) (string, string, error, error) {
			info, invokeErr := service.Disable(ctx, req.ID)
			return appserver.CommandResourcePlugin, info.ID, pluginInfoWarning(info), invokeErr
		}, nil
	case appserver.RemovePluginRequest:
		if action != appserver.ActionPluginRemove {
			break
		}
		return expectedConfigurationRevision(req.ExpectedRevision), func(ctx context.Context, service PluginService) (string, string, error, error) {
			return appserver.CommandResourcePlugin, strings.TrimSpace(req.ID), nil, service.Remove(ctx, req.ID)
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

func (s *controlCommandBackend) currentPluginConfigurationRevision(ctx context.Context) (uint64, bool) {
	if s == nil || s.composition.authorities.store == nil {
		return 0, false
	}
	doc, err := s.composition.authorities.store.LoadContext(contextOrBackground(ctx))
	if err != nil {
		return 0, false
	}
	return doc.ConfigurationRevision, true
}

func classifyPluginMutationError(err error) error {
	if err == nil {
		return nil
	}
	// Managed plugin materialization publishes immutable content and configuration
	// writes use revision CAS. A failed attempt may leave only unreferenced cache
	// content, so a caller can retry with a fresh operation and current revision.
	if errors.Is(err, configstore.ErrConfigurationRevisionConflict) {
		coded := errorcode.Wrap(errorcode.Conflict, "gatewayapp: plugin configuration conflict", err)
		return appserver.NewOutcomeError(appserver.OutcomeConflicted, coded)
	}
	if configstore.WriteCommitted(err) {
		return appserver.NewOutcomeError(appserver.OutcomeUnknown, err)
	}
	if errorcode.CodeOf(err) == errorcode.Unknown {
		err = errorcode.Wrap(errorcode.FailedPrecondition, err.Error(), err)
	}
	return configurationRejectedError(err)
}
