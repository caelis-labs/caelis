package local

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	controladapter "github.com/caelis-labs/caelis/app/gatewayapp/controladapter"
	controlclient "github.com/caelis-labs/caelis/control/client"
)

// PluginService implements Session-authorized, host-owned plugin and
// marketplace configuration inside the AppServer boundary.
type PluginService struct {
	host *gatewayapp.Stack
}

func NewPluginService(host *gatewayapp.Stack) (*PluginService, error) {
	if host == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: host Stack is required")
	}
	return &PluginService{host: host}, nil
}

func (s *PluginService) ListPlugins(ctx context.Context, principal controlclient.Principal, req controlclient.PluginRequest) ([]controlclient.PluginSnapshot, error) {
	driver, err := s.hostAdapter(ctx, principal, req)
	if err != nil {
		return nil, err
	}
	return driver.ListPlugins(ctx)
}

func (s *PluginService) AddMarketplace(ctx context.Context, principal controlclient.Principal, req controlclient.PluginRequest) (controlclient.MarketplaceSnapshot, error) {
	driver, err := s.hostAdapter(ctx, principal, req)
	if err != nil {
		return controlclient.MarketplaceSnapshot{}, err
	}
	return driver.AddMarketplace(ctx, req.Source)
}

func (s *PluginService) ListMarketplaces(ctx context.Context, principal controlclient.Principal, req controlclient.PluginRequest) ([]controlclient.MarketplaceSnapshot, error) {
	driver, err := s.hostAdapter(ctx, principal, req)
	if err != nil {
		return nil, err
	}
	return driver.ListMarketplaces(ctx)
}

func (s *PluginService) UpdateMarketplace(ctx context.Context, principal controlclient.Principal, req controlclient.PluginRequest) (controlclient.MarketplaceSnapshot, error) {
	driver, err := s.hostAdapter(ctx, principal, req)
	if err != nil {
		return controlclient.MarketplaceSnapshot{}, err
	}
	return driver.UpdateMarketplace(ctx, req.Name)
}

func (s *PluginService) RemoveMarketplace(ctx context.Context, principal controlclient.Principal, req controlclient.PluginRequest) error {
	driver, err := s.hostAdapter(ctx, principal, req)
	if err != nil {
		return err
	}
	return driver.RemoveMarketplace(ctx, req.Name)
}

func (s *PluginService) AddPluginPath(ctx context.Context, principal controlclient.Principal, req controlclient.PluginRequest) (controlclient.PluginSnapshot, error) {
	driver, err := s.hostAdapter(ctx, principal, req)
	if err != nil {
		return controlclient.PluginSnapshot{}, err
	}
	return driver.AddPluginPath(ctx, req.Path)
}

func (s *PluginService) InstallPlugin(ctx context.Context, principal controlclient.Principal, req controlclient.PluginRequest) (controlclient.PluginSnapshot, error) {
	driver, err := s.hostAdapter(ctx, principal, req)
	if err != nil {
		return controlclient.PluginSnapshot{}, err
	}
	return driver.InstallPlugin(ctx, req.Source)
}

func (s *PluginService) EnablePlugin(ctx context.Context, principal controlclient.Principal, req controlclient.PluginRequest) (controlclient.PluginSnapshot, error) {
	driver, err := s.hostAdapter(ctx, principal, req)
	if err != nil {
		return controlclient.PluginSnapshot{}, err
	}
	return driver.EnablePlugin(ctx, req.ID)
}

func (s *PluginService) DisablePlugin(ctx context.Context, principal controlclient.Principal, req controlclient.PluginRequest) (controlclient.PluginSnapshot, error) {
	driver, err := s.hostAdapter(ctx, principal, req)
	if err != nil {
		return controlclient.PluginSnapshot{}, err
	}
	return driver.DisablePlugin(ctx, req.ID)
}

func (s *PluginService) RemovePlugin(ctx context.Context, principal controlclient.Principal, req controlclient.PluginRequest) error {
	driver, err := s.hostAdapter(ctx, principal, req)
	if err != nil {
		return err
	}
	return driver.RemovePlugin(ctx, req.ID)
}

func (s *PluginService) InspectPlugin(ctx context.Context, principal controlclient.Principal, req controlclient.PluginRequest) (controlclient.PluginSnapshot, error) {
	driver, err := s.hostAdapter(ctx, principal, req)
	if err != nil {
		return controlclient.PluginSnapshot{}, err
	}
	return driver.InspectPlugin(ctx, req.ID)
}

func (s *PluginService) hostAdapter(ctx context.Context, principal controlclient.Principal, req controlclient.PluginRequest) (controladapter.PluginAssembler, error) {
	if s == nil || s.host == nil || s.host.Sessions == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: plugin service is unavailable")
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if err := (controlclient.SessionAuthorizer{Sessions: s.host.Sessions}).Authorize(ctx, principal, controlclient.ActionSessionInspect, sessionID); err != nil {
		return nil, err
	}
	active, err := s.host.Sessions.Session(ctx, session.SessionRef{SessionID: sessionID})
	if err != nil {
		return nil, err
	}
	return controladapter.NewPluginAssemblerForSession(ctx, runtimeStack(s.host), active, strings.TrimSpace(req.Surface), "")
}

var _ controlclient.PluginService = (*PluginService)(nil)
