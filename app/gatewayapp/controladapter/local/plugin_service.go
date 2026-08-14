package local

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	controladapter "github.com/caelis-labs/caelis/app/gatewayapp/controladapter"
	controlclient "github.com/caelis-labs/caelis/control/client"
)

// PluginService implements Host-owned plugin and marketplace configuration
// inside the AppServer boundary. Reads stay pure observations; mutations use
// the shared recoverable command path.
type PluginService struct {
	host     *gatewayapp.Stack
	commands controlclient.PluginCommandService
}

func NewPluginService(host *gatewayapp.Stack) (*PluginService, error) {
	if host == nil || host.PluginCommands() == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: host Stack is required")
	}
	return &PluginService{host: host, commands: host.PluginCommands()}, nil
}

func (s *PluginService) ListPlugins(ctx context.Context, principal controlclient.Principal, req controlclient.PluginRequest) ([]controlclient.PluginSnapshot, error) {
	driver, err := s.hostAdapter(principal, req)
	if err != nil {
		return nil, err
	}
	return driver.ListPlugins(ctx)
}

func (s *PluginService) AddMarketplace(ctx context.Context, principal controlclient.Principal, req controlclient.AddMarketplaceRequest) (controlclient.CommandResult, error) {
	return s.commands.AddMarketplace(ctx, principal, req)
}

func (s *PluginService) ListMarketplaces(ctx context.Context, principal controlclient.Principal, req controlclient.PluginRequest) ([]controlclient.MarketplaceSnapshot, error) {
	driver, err := s.hostAdapter(principal, req)
	if err != nil {
		return nil, err
	}
	return driver.ListMarketplaces(ctx)
}

func (s *PluginService) UpdateMarketplace(ctx context.Context, principal controlclient.Principal, req controlclient.UpdateMarketplaceRequest) (controlclient.CommandResult, error) {
	return s.commands.UpdateMarketplace(ctx, principal, req)
}

func (s *PluginService) RemoveMarketplace(ctx context.Context, principal controlclient.Principal, req controlclient.RemoveMarketplaceRequest) (controlclient.CommandResult, error) {
	return s.commands.RemoveMarketplace(ctx, principal, req)
}

func (s *PluginService) AddPluginPath(ctx context.Context, principal controlclient.Principal, req controlclient.AddPluginPathRequest) (controlclient.CommandResult, error) {
	return s.commands.AddPluginPath(ctx, principal, req)
}

func (s *PluginService) InstallPlugin(ctx context.Context, principal controlclient.Principal, req controlclient.InstallPluginRequest) (controlclient.CommandResult, error) {
	return s.commands.InstallPlugin(ctx, principal, req)
}

func (s *PluginService) EnablePlugin(ctx context.Context, principal controlclient.Principal, req controlclient.EnablePluginRequest) (controlclient.CommandResult, error) {
	return s.commands.EnablePlugin(ctx, principal, req)
}

func (s *PluginService) DisablePlugin(ctx context.Context, principal controlclient.Principal, req controlclient.DisablePluginRequest) (controlclient.CommandResult, error) {
	return s.commands.DisablePlugin(ctx, principal, req)
}

func (s *PluginService) RemovePlugin(ctx context.Context, principal controlclient.Principal, req controlclient.RemovePluginRequest) (controlclient.CommandResult, error) {
	return s.commands.RemovePlugin(ctx, principal, req)
}

func (s *PluginService) InspectPlugin(ctx context.Context, principal controlclient.Principal, req controlclient.PluginRequest) (controlclient.PluginSnapshot, error) {
	driver, err := s.hostAdapter(principal, req)
	if err != nil {
		return controlclient.PluginSnapshot{}, err
	}
	return driver.InspectPlugin(ctx, req.ID)
}

func (s *PluginService) hostAdapter(principal controlclient.Principal, req controlclient.PluginRequest) (controladapter.PluginAssembler, error) {
	if s == nil || s.host == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: plugin service is unavailable")
	}
	if err := authorizeHostCapability(principal); err != nil {
		return nil, err
	}
	return controladapter.NewPluginAssemblerForStack(runtimeStack(s.host), strings.TrimSpace(req.Surface), ""), nil
}

var _ controlclient.PluginService = (*PluginService)(nil)
