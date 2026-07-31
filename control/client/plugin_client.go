package controlclient

import (
	"context"
	"errors"
	"strings"
)

// PluginRequest addresses one typed plugin or marketplace operation. Source,
// Name, ID, and Path are operation-specific; raw slash input is never carried.
type PluginRequest struct {
	SessionID string `json:"session_id"`
	Surface   string `json:"surface,omitempty"`
	Source    string `json:"source,omitempty"`
	Name      string `json:"name,omitempty"`
	ID        string `json:"id,omitempty"`
	Path      string `json:"path,omitempty"`
}

// PluginService is the principal-aware AppServer plugin configuration
// capability. Mutations remain host-owned after Session authorization.
type PluginService interface {
	ListPlugins(context.Context, Principal, PluginRequest) ([]PluginSnapshot, error)
	AddMarketplace(context.Context, Principal, PluginRequest) (MarketplaceSnapshot, error)
	ListMarketplaces(context.Context, Principal, PluginRequest) ([]MarketplaceSnapshot, error)
	UpdateMarketplace(context.Context, Principal, PluginRequest) (MarketplaceSnapshot, error)
	RemoveMarketplace(context.Context, Principal, PluginRequest) error
	AddPluginPath(context.Context, Principal, PluginRequest) (PluginSnapshot, error)
	InstallPlugin(context.Context, Principal, PluginRequest) (PluginSnapshot, error)
	EnablePlugin(context.Context, Principal, PluginRequest) (PluginSnapshot, error)
	DisablePlugin(context.Context, Principal, PluginRequest) (PluginSnapshot, error)
	RemovePlugin(context.Context, Principal, PluginRequest) error
	InspectPlugin(context.Context, Principal, PluginRequest) (PluginSnapshot, error)
}

type PluginClient interface {
	ListPlugins(context.Context, PluginRequest) ([]PluginSnapshot, error)
	AddMarketplace(context.Context, PluginRequest) (MarketplaceSnapshot, error)
	ListMarketplaces(context.Context, PluginRequest) ([]MarketplaceSnapshot, error)
	UpdateMarketplace(context.Context, PluginRequest) (MarketplaceSnapshot, error)
	RemoveMarketplace(context.Context, PluginRequest) error
	AddPluginPath(context.Context, PluginRequest) (PluginSnapshot, error)
	InstallPlugin(context.Context, PluginRequest) (PluginSnapshot, error)
	EnablePlugin(context.Context, PluginRequest) (PluginSnapshot, error)
	DisablePlugin(context.Context, PluginRequest) (PluginSnapshot, error)
	RemovePlugin(context.Context, PluginRequest) error
	InspectPlugin(context.Context, PluginRequest) (PluginSnapshot, error)
}

type boundPluginClient struct {
	service   PluginService
	principal Principal
}

func BindPluginClient(service PluginService, principal Principal) (PluginClient, error) {
	if service == nil {
		return nil, errors.New("controlclient: plugin service is required")
	}
	principal.ID = strings.TrimSpace(principal.ID)
	if principal.ID == "" {
		return nil, errors.New("controlclient: principal ID is required")
	}
	principal.Roles = append([]string(nil), principal.Roles...)
	return &boundPluginClient{service: service, principal: principal}, nil
}

func (c *boundPluginClient) boundPrincipal() Principal {
	principal := c.principal
	principal.Roles = append([]string(nil), principal.Roles...)
	return principal
}

func (c *boundPluginClient) ListPlugins(ctx context.Context, req PluginRequest) ([]PluginSnapshot, error) {
	return c.service.ListPlugins(ctx, c.boundPrincipal(), req)
}
func (c *boundPluginClient) AddMarketplace(ctx context.Context, req PluginRequest) (MarketplaceSnapshot, error) {
	return c.service.AddMarketplace(ctx, c.boundPrincipal(), req)
}
func (c *boundPluginClient) ListMarketplaces(ctx context.Context, req PluginRequest) ([]MarketplaceSnapshot, error) {
	return c.service.ListMarketplaces(ctx, c.boundPrincipal(), req)
}
func (c *boundPluginClient) UpdateMarketplace(ctx context.Context, req PluginRequest) (MarketplaceSnapshot, error) {
	return c.service.UpdateMarketplace(ctx, c.boundPrincipal(), req)
}
func (c *boundPluginClient) RemoveMarketplace(ctx context.Context, req PluginRequest) error {
	return c.service.RemoveMarketplace(ctx, c.boundPrincipal(), req)
}
func (c *boundPluginClient) AddPluginPath(ctx context.Context, req PluginRequest) (PluginSnapshot, error) {
	return c.service.AddPluginPath(ctx, c.boundPrincipal(), req)
}
func (c *boundPluginClient) InstallPlugin(ctx context.Context, req PluginRequest) (PluginSnapshot, error) {
	return c.service.InstallPlugin(ctx, c.boundPrincipal(), req)
}
func (c *boundPluginClient) EnablePlugin(ctx context.Context, req PluginRequest) (PluginSnapshot, error) {
	return c.service.EnablePlugin(ctx, c.boundPrincipal(), req)
}
func (c *boundPluginClient) DisablePlugin(ctx context.Context, req PluginRequest) (PluginSnapshot, error) {
	return c.service.DisablePlugin(ctx, c.boundPrincipal(), req)
}
func (c *boundPluginClient) RemovePlugin(ctx context.Context, req PluginRequest) error {
	return c.service.RemovePlugin(ctx, c.boundPrincipal(), req)
}
func (c *boundPluginClient) InspectPlugin(ctx context.Context, req PluginRequest) (PluginSnapshot, error) {
	return c.service.InspectPlugin(ctx, c.boundPrincipal(), req)
}

var _ PluginClient = (*boundPluginClient)(nil)
