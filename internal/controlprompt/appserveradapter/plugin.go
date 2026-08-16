package appserveradapter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func (a *SessionClientAdapter) ListPlugins(ctx context.Context) ([]controlprompt.PluginSnapshot, error) {
	request, err := a.pluginRequest(ctx)
	if err != nil {
		return nil, err
	}
	return a.pluginClient.ListPlugins(ctx, request)
}

func (a *SessionClientAdapter) AddMarketplace(ctx context.Context, source string) (controlprompt.MarketplaceSnapshot, error) {
	return a.runMarketplaceMutation(ctx, "marketplace add", "plugin-marketplace-add", func(base appserver.WriteBase) (appserver.CommandResult, error) {
		return a.pluginClient.AddMarketplace(ctx, appserver.AddMarketplaceRequest{WriteBase: base, Source: strings.TrimSpace(source)})
	}, func(ctx context.Context) (controlprompt.MarketplaceSnapshot, error) {
		marketplaces, err := a.ListMarketplaces(ctx)
		if err != nil {
			return controlprompt.MarketplaceSnapshot{}, err
		}
		source = strings.TrimSpace(source)
		for _, marketplace := range marketplaces {
			if strings.EqualFold(strings.TrimSpace(marketplace.Source), source) ||
				strings.EqualFold(strings.TrimSpace(marketplace.Name), source) {
				return marketplace, nil
			}
		}
		if len(marketplaces) == 0 {
			return controlprompt.MarketplaceSnapshot{}, errors.New("app/gatewayapp/controladapter: marketplace committed but observation is empty")
		}
		return marketplaces[len(marketplaces)-1], nil
	})
}

func (a *SessionClientAdapter) ListMarketplaces(ctx context.Context) ([]controlprompt.MarketplaceSnapshot, error) {
	request, err := a.pluginRequest(ctx)
	if err != nil {
		return nil, err
	}
	return a.pluginClient.ListMarketplaces(ctx, request)
}

func (a *SessionClientAdapter) UpdateMarketplace(ctx context.Context, name string) (controlprompt.MarketplaceSnapshot, error) {
	name = strings.TrimSpace(name)
	return a.runMarketplaceMutation(ctx, "marketplace update", "plugin-marketplace-update", func(base appserver.WriteBase) (appserver.CommandResult, error) {
		return a.pluginClient.UpdateMarketplace(ctx, appserver.UpdateMarketplaceRequest{WriteBase: base, Name: name})
	}, func(ctx context.Context) (controlprompt.MarketplaceSnapshot, error) {
		marketplaces, err := a.ListMarketplaces(ctx)
		if err != nil {
			return controlprompt.MarketplaceSnapshot{}, err
		}
		for _, marketplace := range marketplaces {
			if strings.EqualFold(strings.TrimSpace(marketplace.Name), name) {
				return marketplace, nil
			}
		}
		return controlprompt.MarketplaceSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: marketplace %q committed but observation failed", name)
	})
}

func (a *SessionClientAdapter) RemoveMarketplace(ctx context.Context, name string) error {
	_, err := a.runPluginMutation(ctx, "marketplace remove", "plugin-marketplace-remove", func(base appserver.WriteBase) (appserver.CommandResult, error) {
		return a.pluginClient.RemoveMarketplace(ctx, appserver.RemoveMarketplaceRequest{WriteBase: base, Name: strings.TrimSpace(name)})
	}, nil)
	return err
}

func (a *SessionClientAdapter) AddPluginPath(ctx context.Context, path string) (controlprompt.PluginSnapshot, error) {
	return a.runPluginMutation(ctx, "plugin add-path", "plugin-add-path", func(base appserver.WriteBase) (appserver.CommandResult, error) {
		return a.pluginClient.AddPluginPath(ctx, appserver.AddPluginPathRequest{WriteBase: base, Path: strings.TrimSpace(path)})
	}, a.observePluginResource)
}

func (a *SessionClientAdapter) InstallPlugin(ctx context.Context, source string) (controlprompt.PluginSnapshot, error) {
	return a.runPluginMutation(ctx, "plugin install", "plugin-install", func(base appserver.WriteBase) (appserver.CommandResult, error) {
		return a.pluginClient.InstallPlugin(ctx, appserver.InstallPluginRequest{WriteBase: base, Source: strings.TrimSpace(source)})
	}, a.observePluginResource)
}

func (a *SessionClientAdapter) EnablePlugin(ctx context.Context, id string) (controlprompt.PluginSnapshot, error) {
	id = strings.TrimSpace(id)
	return a.runPluginMutation(ctx, "plugin enable", "plugin-enable", func(base appserver.WriteBase) (appserver.CommandResult, error) {
		return a.pluginClient.EnablePlugin(ctx, appserver.EnablePluginRequest{WriteBase: base, ID: id})
	}, a.observePluginResource)
}

func (a *SessionClientAdapter) DisablePlugin(ctx context.Context, id string) (controlprompt.PluginSnapshot, error) {
	id = strings.TrimSpace(id)
	return a.runPluginMutation(ctx, "plugin disable", "plugin-disable", func(base appserver.WriteBase) (appserver.CommandResult, error) {
		return a.pluginClient.DisablePlugin(ctx, appserver.DisablePluginRequest{WriteBase: base, ID: id})
	}, a.observePluginResource)
}

func (a *SessionClientAdapter) RemovePlugin(ctx context.Context, id string) error {
	_, err := a.runPluginMutation(ctx, "plugin remove", "plugin-remove", func(base appserver.WriteBase) (appserver.CommandResult, error) {
		return a.pluginClient.RemovePlugin(ctx, appserver.RemovePluginRequest{WriteBase: base, ID: strings.TrimSpace(id)})
	}, nil)
	return err
}

func (a *SessionClientAdapter) InspectPlugin(ctx context.Context, id string) (controlprompt.PluginSnapshot, error) {
	request, err := a.pluginRequest(ctx)
	if err != nil {
		return controlprompt.PluginSnapshot{}, err
	}
	request.ID = strings.TrimSpace(id)
	return a.pluginClient.InspectPlugin(ctx, request)
}

func (a *SessionClientAdapter) runMarketplaceMutation(
	ctx context.Context,
	label, operationPrefix string,
	command func(appserver.WriteBase) (appserver.CommandResult, error),
	observe func(context.Context) (controlprompt.MarketplaceSnapshot, error),
) (controlprompt.MarketplaceSnapshot, error) {
	if a == nil || a.pluginClient == nil || a.statusClient == nil {
		return controlprompt.MarketplaceSnapshot{}, errors.New("app/gatewayapp/controladapter: plugin clients are unavailable")
	}
	before, err := a.addressedStatus(ctx, "", false)
	if err != nil {
		return controlprompt.MarketplaceSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: read Host configuration revision: %w", err)
	}
	revision := before.Configuration.Revision
	result, commandErr := command(appserver.WriteBase{
		OperationID:      operationPrefix + "-" + uuid.NewString(),
		ExpectedRevision: &revision,
	})
	if result.Outcome != appserver.OutcomeCommitted {
		if commandErr == nil {
			commandErr = fmt.Errorf("app/gatewayapp/controladapter: %s outcome is %q: %s", label, result.Outcome, strings.TrimSpace(result.Detail))
		}
		return controlprompt.MarketplaceSnapshot{}, &appserver.CommandReceiptError{Receipt: result, Err: commandErr}
	}
	observed, observationErr := observe(ctx)
	if observationErr != nil {
		observationErr = fmt.Errorf("app/gatewayapp/controladapter: %s committed as operation %q but marketplace observation failed; do not retry blindly: %w", label, result.OperationID, observationErr)
	}
	resultErr := errors.Join(commandErr, observationErr)
	if detail := strings.TrimSpace(result.Detail); detail != "" && resultErr == nil {
		resultErr = fmt.Errorf("app/gatewayapp/controladapter: %s committed as operation %q with a warning; do not retry blindly: %s", label, result.OperationID, detail)
	}
	if resultErr != nil {
		resultErr = &appserver.CommandReceiptError{Receipt: result, Err: resultErr}
	}
	return observed, resultErr
}

func (a *SessionClientAdapter) runPluginMutation(
	ctx context.Context,
	label, operationPrefix string,
	command func(appserver.WriteBase) (appserver.CommandResult, error),
	observe func(context.Context, appserver.CommandResult) (controlprompt.PluginSnapshot, error),
) (controlprompt.PluginSnapshot, error) {
	if a == nil || a.pluginClient == nil || a.statusClient == nil {
		return controlprompt.PluginSnapshot{}, errors.New("app/gatewayapp/controladapter: plugin clients are unavailable")
	}
	before, err := a.addressedStatus(ctx, "", false)
	if err != nil {
		return controlprompt.PluginSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: read Host configuration revision: %w", err)
	}
	revision := before.Configuration.Revision
	result, commandErr := command(appserver.WriteBase{
		OperationID:      operationPrefix + "-" + uuid.NewString(),
		ExpectedRevision: &revision,
	})
	if result.Outcome != appserver.OutcomeCommitted {
		if commandErr == nil {
			commandErr = fmt.Errorf("app/gatewayapp/controladapter: %s outcome is %q: %s", label, result.Outcome, strings.TrimSpace(result.Detail))
		}
		return controlprompt.PluginSnapshot{}, &appserver.CommandReceiptError{Receipt: result, Err: commandErr}
	}
	var observed controlprompt.PluginSnapshot
	var observationErr error
	if observe != nil {
		observed, observationErr = observe(ctx, result)
		if observationErr != nil {
			observationErr = fmt.Errorf("app/gatewayapp/controladapter: %s committed as operation %q but plugin observation failed; do not retry blindly: %w", label, result.OperationID, observationErr)
		}
	}
	resultErr := errors.Join(commandErr, observationErr)
	if detail := strings.TrimSpace(result.Detail); detail != "" && resultErr == nil {
		resultErr = fmt.Errorf("app/gatewayapp/controladapter: %s committed as operation %q with a warning; do not retry blindly: %s", label, result.OperationID, detail)
	}
	if resultErr != nil {
		resultErr = &appserver.CommandReceiptError{Receipt: result, Err: resultErr}
	}
	return observed, resultErr
}

func (a *SessionClientAdapter) observePluginResource(ctx context.Context, result appserver.CommandResult) (controlprompt.PluginSnapshot, error) {
	if result.Resource == nil || strings.TrimSpace(result.Resource.Ref) == "" {
		return controlprompt.PluginSnapshot{}, errors.New("app/gatewayapp/controladapter: plugin mutation committed without a resource identity")
	}
	return a.InspectPlugin(ctx, result.Resource.Ref)
}

func (a *SessionClientAdapter) pluginRequest(context.Context) (appserver.PluginRequest, error) {
	if a == nil || a.pluginClient == nil {
		return appserver.PluginRequest{}, errors.New("app/gatewayapp/controladapter: plugin client is unavailable")
	}
	return appserver.PluginRequest{Surface: a.surface}, nil
}
