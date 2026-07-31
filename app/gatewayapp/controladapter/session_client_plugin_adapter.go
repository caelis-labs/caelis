package controladapter

import (
	"context"
	"errors"
	"strings"

	controlclient "github.com/caelis-labs/caelis/control/client"
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
	request, err := a.pluginRequest(ctx)
	if err != nil {
		return controlprompt.MarketplaceSnapshot{}, err
	}
	request.Source = strings.TrimSpace(source)
	return a.pluginClient.AddMarketplace(ctx, request)
}

func (a *SessionClientAdapter) ListMarketplaces(ctx context.Context) ([]controlprompt.MarketplaceSnapshot, error) {
	request, err := a.pluginRequest(ctx)
	if err != nil {
		return nil, err
	}
	return a.pluginClient.ListMarketplaces(ctx, request)
}

func (a *SessionClientAdapter) UpdateMarketplace(ctx context.Context, name string) (controlprompt.MarketplaceSnapshot, error) {
	request, err := a.pluginRequest(ctx)
	if err != nil {
		return controlprompt.MarketplaceSnapshot{}, err
	}
	request.Name = strings.TrimSpace(name)
	return a.pluginClient.UpdateMarketplace(ctx, request)
}

func (a *SessionClientAdapter) RemoveMarketplace(ctx context.Context, name string) error {
	request, err := a.pluginRequest(ctx)
	if err != nil {
		return err
	}
	request.Name = strings.TrimSpace(name)
	return a.pluginClient.RemoveMarketplace(ctx, request)
}

func (a *SessionClientAdapter) AddPluginPath(ctx context.Context, path string) (controlprompt.PluginSnapshot, error) {
	request, err := a.pluginRequest(ctx)
	if err != nil {
		return controlprompt.PluginSnapshot{}, err
	}
	request.Path = strings.TrimSpace(path)
	return a.pluginClient.AddPluginPath(ctx, request)
}

func (a *SessionClientAdapter) InstallPlugin(ctx context.Context, source string) (controlprompt.PluginSnapshot, error) {
	request, err := a.pluginRequest(ctx)
	if err != nil {
		return controlprompt.PluginSnapshot{}, err
	}
	request.Source = strings.TrimSpace(source)
	return a.pluginClient.InstallPlugin(ctx, request)
}

func (a *SessionClientAdapter) EnablePlugin(ctx context.Context, id string) (controlprompt.PluginSnapshot, error) {
	return a.mutatePlugin(ctx, id, "enable")
}

func (a *SessionClientAdapter) DisablePlugin(ctx context.Context, id string) (controlprompt.PluginSnapshot, error) {
	return a.mutatePlugin(ctx, id, "disable")
}

func (a *SessionClientAdapter) RemovePlugin(ctx context.Context, id string) error {
	request, err := a.pluginRequest(ctx)
	if err != nil {
		return err
	}
	request.ID = strings.TrimSpace(id)
	return a.pluginClient.RemovePlugin(ctx, request)
}

func (a *SessionClientAdapter) InspectPlugin(ctx context.Context, id string) (controlprompt.PluginSnapshot, error) {
	return a.mutatePlugin(ctx, id, "inspect")
}

func (a *SessionClientAdapter) mutatePlugin(ctx context.Context, id, action string) (controlprompt.PluginSnapshot, error) {
	request, err := a.pluginRequest(ctx)
	if err != nil {
		return controlprompt.PluginSnapshot{}, err
	}
	request.ID = strings.TrimSpace(id)
	switch action {
	case "enable":
		return a.pluginClient.EnablePlugin(ctx, request)
	case "disable":
		return a.pluginClient.DisablePlugin(ctx, request)
	case "inspect":
		return a.pluginClient.InspectPlugin(ctx, request)
	default:
		return controlprompt.PluginSnapshot{}, errors.New("app/gatewayapp/controladapter: unknown plugin action")
	}
}

func (a *SessionClientAdapter) pluginRequest(ctx context.Context) (controlclient.PluginRequest, error) {
	if a == nil || a.pluginClient == nil {
		return controlclient.PluginRequest{}, errors.New("app/gatewayapp/controladapter: plugin client is unavailable")
	}
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return controlclient.PluginRequest{}, err
	}
	return controlclient.PluginRequest{SessionID: state.SessionID, Surface: a.surface}, nil
}
