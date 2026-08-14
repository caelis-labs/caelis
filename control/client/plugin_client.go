package controlclient

import (
	"context"
	"errors"
	"strings"
)

// PluginRequest addresses one Host-owned plugin or marketplace read.
// SessionID is retained as optional wire compatibility context and is ignored
// by Host-scoped service methods.
type PluginRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Surface   string `json:"surface,omitempty"`
	Source    string `json:"source,omitempty"`
	Name      string `json:"name,omitempty"`
	ID        string `json:"id,omitempty"`
	Path      string `json:"path,omitempty"`
}

// AddMarketplaceRequest registers one marketplace source under Host
// configuration revision CAS. Marketplace fetch is an external effect.
type AddMarketplaceRequest struct {
	WriteBase
	Source string `json:"source"`
}

// UpdateMarketplaceRequest refreshes one registered marketplace. Marketplace
// fetch is an external effect.
type UpdateMarketplaceRequest struct {
	WriteBase
	Name string `json:"name"`
}

// RemoveMarketplaceRequest removes one registered marketplace from AppConfig.
type RemoveMarketplaceRequest struct {
	WriteBase
	Name string `json:"name"`
}

// AddPluginPathRequest registers one local plugin directory in AppConfig.
type AddPluginPathRequest struct {
	WriteBase
	Path string `json:"path"`
}

// InstallPluginRequest installs one plugin from a marketplace or local source.
// Managed install/cache materialization is an external effect.
type InstallPluginRequest struct {
	WriteBase
	Source string `json:"source"`
}

// EnablePluginRequest enables one configured plugin for future activations.
type EnablePluginRequest struct {
	WriteBase
	ID string `json:"id"`
}

// DisablePluginRequest disables one configured plugin for future activations.
type DisablePluginRequest struct {
	WriteBase
	ID string `json:"id"`
}

// RemovePluginRequest removes one configured plugin from AppConfig.
type RemovePluginRequest struct {
	WriteBase
	ID string `json:"id"`
}

// PluginService is the principal-aware AppServer plugin configuration
// capability. Reads stay pure observations; mutations return CommandResult and
// are Host-owned after principal authorization.
type PluginService interface {
	ListPlugins(context.Context, Principal, PluginRequest) ([]PluginSnapshot, error)
	AddMarketplace(context.Context, Principal, AddMarketplaceRequest) (CommandResult, error)
	ListMarketplaces(context.Context, Principal, PluginRequest) ([]MarketplaceSnapshot, error)
	UpdateMarketplace(context.Context, Principal, UpdateMarketplaceRequest) (CommandResult, error)
	RemoveMarketplace(context.Context, Principal, RemoveMarketplaceRequest) (CommandResult, error)
	AddPluginPath(context.Context, Principal, AddPluginPathRequest) (CommandResult, error)
	InstallPlugin(context.Context, Principal, InstallPluginRequest) (CommandResult, error)
	EnablePlugin(context.Context, Principal, EnablePluginRequest) (CommandResult, error)
	DisablePlugin(context.Context, Principal, DisablePluginRequest) (CommandResult, error)
	RemovePlugin(context.Context, Principal, RemovePluginRequest) (CommandResult, error)
	InspectPlugin(context.Context, Principal, PluginRequest) (PluginSnapshot, error)
}

type PluginClient interface {
	ListPlugins(context.Context, PluginRequest) ([]PluginSnapshot, error)
	AddMarketplace(context.Context, AddMarketplaceRequest) (CommandResult, error)
	ListMarketplaces(context.Context, PluginRequest) ([]MarketplaceSnapshot, error)
	UpdateMarketplace(context.Context, UpdateMarketplaceRequest) (CommandResult, error)
	RemoveMarketplace(context.Context, RemoveMarketplaceRequest) (CommandResult, error)
	AddPluginPath(context.Context, AddPluginPathRequest) (CommandResult, error)
	InstallPlugin(context.Context, InstallPluginRequest) (CommandResult, error)
	EnablePlugin(context.Context, EnablePluginRequest) (CommandResult, error)
	DisablePlugin(context.Context, DisablePluginRequest) (CommandResult, error)
	RemovePlugin(context.Context, RemovePluginRequest) (CommandResult, error)
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
func (c *boundPluginClient) AddMarketplace(ctx context.Context, req AddMarketplaceRequest) (CommandResult, error) {
	return c.service.AddMarketplace(ctx, c.boundPrincipal(), req)
}
func (c *boundPluginClient) ListMarketplaces(ctx context.Context, req PluginRequest) ([]MarketplaceSnapshot, error) {
	return c.service.ListMarketplaces(ctx, c.boundPrincipal(), req)
}
func (c *boundPluginClient) UpdateMarketplace(ctx context.Context, req UpdateMarketplaceRequest) (CommandResult, error) {
	return c.service.UpdateMarketplace(ctx, c.boundPrincipal(), req)
}
func (c *boundPluginClient) RemoveMarketplace(ctx context.Context, req RemoveMarketplaceRequest) (CommandResult, error) {
	return c.service.RemoveMarketplace(ctx, c.boundPrincipal(), req)
}
func (c *boundPluginClient) AddPluginPath(ctx context.Context, req AddPluginPathRequest) (CommandResult, error) {
	return c.service.AddPluginPath(ctx, c.boundPrincipal(), req)
}
func (c *boundPluginClient) InstallPlugin(ctx context.Context, req InstallPluginRequest) (CommandResult, error) {
	return c.service.InstallPlugin(ctx, c.boundPrincipal(), req)
}
func (c *boundPluginClient) EnablePlugin(ctx context.Context, req EnablePluginRequest) (CommandResult, error) {
	return c.service.EnablePlugin(ctx, c.boundPrincipal(), req)
}
func (c *boundPluginClient) DisablePlugin(ctx context.Context, req DisablePluginRequest) (CommandResult, error) {
	return c.service.DisablePlugin(ctx, c.boundPrincipal(), req)
}
func (c *boundPluginClient) RemovePlugin(ctx context.Context, req RemovePluginRequest) (CommandResult, error) {
	return c.service.RemovePlugin(ctx, c.boundPrincipal(), req)
}
func (c *boundPluginClient) InspectPlugin(ctx context.Context, req PluginRequest) (PluginSnapshot, error) {
	return c.service.InspectPlugin(ctx, c.boundPrincipal(), req)
}

var _ PluginClient = (*boundPluginClient)(nil)
