package local

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	controladapter "github.com/caelis-labs/caelis/app/gatewayapp/controladapter"
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

// PluginService implements Host-owned plugin and marketplace configuration
// inside the AppServer boundary. Reads stay pure observations; mutations use
// the shared recoverable command path.
type PluginService struct {
	host     *gatewayapp.Stack
	commands appserver.PluginCommandService
}

func NewPluginService(host *gatewayapp.Stack) (*PluginService, error) {
	if host == nil || host.PluginCommands() == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: host Stack is required")
	}
	return &PluginService{host: host, commands: host.PluginCommands()}, nil
}

func (s *PluginService) ListPlugins(ctx context.Context, principal appserver.Principal, req appserver.PluginRequest) ([]appserver.PluginSnapshot, error) {
	driver, err := s.hostAdapter(principal, req)
	if err != nil {
		return nil, err
	}
	return driver.ListPlugins(ctx)
}

func (s *PluginService) AddMarketplace(ctx context.Context, principal appserver.Principal, req appserver.AddMarketplaceRequest) (appserver.CommandResult, error) {
	return s.commands.AddMarketplace(ctx, principal, req)
}

func (s *PluginService) ListMarketplaces(ctx context.Context, principal appserver.Principal, req appserver.PluginRequest) ([]appserver.MarketplaceSnapshot, error) {
	driver, err := s.hostAdapter(principal, req)
	if err != nil {
		return nil, err
	}
	return driver.ListMarketplaces(ctx)
}

func (s *PluginService) UpdateMarketplace(ctx context.Context, principal appserver.Principal, req appserver.UpdateMarketplaceRequest) (appserver.CommandResult, error) {
	return s.commands.UpdateMarketplace(ctx, principal, req)
}

func (s *PluginService) RemoveMarketplace(ctx context.Context, principal appserver.Principal, req appserver.RemoveMarketplaceRequest) (appserver.CommandResult, error) {
	return s.commands.RemoveMarketplace(ctx, principal, req)
}

func (s *PluginService) AddPluginPath(ctx context.Context, principal appserver.Principal, req appserver.AddPluginPathRequest) (appserver.CommandResult, error) {
	return s.commands.AddPluginPath(ctx, principal, req)
}

func (s *PluginService) InstallPlugin(ctx context.Context, principal appserver.Principal, req appserver.InstallPluginRequest) (appserver.CommandResult, error) {
	return s.commands.InstallPlugin(ctx, principal, req)
}

func (s *PluginService) EnablePlugin(ctx context.Context, principal appserver.Principal, req appserver.EnablePluginRequest) (appserver.CommandResult, error) {
	return s.commands.EnablePlugin(ctx, principal, req)
}

func (s *PluginService) DisablePlugin(ctx context.Context, principal appserver.Principal, req appserver.DisablePluginRequest) (appserver.CommandResult, error) {
	return s.commands.DisablePlugin(ctx, principal, req)
}

func (s *PluginService) RemovePlugin(ctx context.Context, principal appserver.Principal, req appserver.RemovePluginRequest) (appserver.CommandResult, error) {
	return s.commands.RemovePlugin(ctx, principal, req)
}

func (s *PluginService) InspectPlugin(ctx context.Context, principal appserver.Principal, req appserver.PluginRequest) (appserver.PluginSnapshot, error) {
	driver, err := s.hostAdapter(principal, req)
	if err != nil {
		return appserver.PluginSnapshot{}, err
	}
	return driver.InspectPlugin(ctx, req.ID)
}

func (s *PluginService) hostAdapter(principal appserver.Principal, req appserver.PluginRequest) (controladapter.PluginAssembler, error) {
	if s == nil || s.host == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: plugin service is unavailable")
	}
	if err := authorizeHostCapability(principal); err != nil {
		return nil, err
	}
	return controladapter.NewPluginAssemblerForHost(controlRuntimeDeps(s.host), strings.TrimSpace(req.Surface), ""), nil
}

var _ appserver.PluginService = (*PluginService)(nil)
