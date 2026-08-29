package appserver

import (
	"context"
	"errors"
	"strings"
)

type SessionModeRequest struct {
	WriteBase
	Mode string `json:"mode"`
}

// SessionModelRequest selects one model and reasoning effort for exactly one
// Session. It never changes the Host default model.
type SessionModelRequest struct {
	WriteBase
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// Clear removes the Session override when no provider model remains.
	// Model and ReasoningEffort must be empty when Clear is true.
	Clear bool `json:"clear,omitempty"`
}

// SessionControllerModeRequest selects one mode on the currently bound
// external ACP controller. The controller epoch is mandatory and exact.
type SessionControllerModeRequest struct {
	WriteBase
	Mode string `json:"mode"`
}

// SessionPresentationModeRequest selects one app-owned ACP presentation mode.
// It is distinct from the approval-routing mode above.
type SessionPresentationModeRequest struct {
	WriteBase
	Mode string `json:"mode"`
}

// SessionPresentationConfigRequest selects one app-owned ACP configuration
// option. Product presentation configuration is currently select/string-only.
type SessionPresentationConfigRequest struct {
	WriteBase
	ConfigID string `json:"config_id"`
	Value    string `json:"value"`
}

type ConnectModelRequest struct {
	WriteBase
	Config ConnectConfig `json:"config"`
}

type UseModelRequest struct {
	WriteBase
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type DeleteModelRequest struct {
	WriteBase
	Model string `json:"model"`
}

type SandboxRequest struct {
	WriteBase
	Backend string `json:"backend,omitempty"`
}

// ConfigurationService owns typed AppServer configuration operations. It is a
// transport contract; model, sandbox, and Session policy semantics remain with
// their existing Control owners.
type ConfigurationService interface {
	ConfigureSessionMode(context.Context, Principal, SessionModeRequest) (CommandResult, error)
	UseSessionModel(context.Context, Principal, SessionModelRequest) (CommandResult, error)
	ConfigureSessionControllerMode(context.Context, Principal, SessionControllerModeRequest) (CommandResult, error)
	ConfigureSessionPresentationMode(context.Context, Principal, SessionPresentationModeRequest) (CommandResult, error)
	ConfigureSessionPresentation(context.Context, Principal, SessionPresentationConfigRequest) (CommandResult, error)
	ConnectModel(context.Context, Principal, ConnectModelRequest) (CommandResult, error)
	UseModel(context.Context, Principal, UseModelRequest) (CommandResult, error)
	DeleteModel(context.Context, Principal, DeleteModelRequest) (CommandResult, error)
	SetSandboxBackend(context.Context, Principal, SandboxRequest) (CommandResult, error)
	PrepareSandbox(context.Context, Principal, SandboxRequest) (CommandResult, error)
	RepairSandbox(context.Context, Principal, SandboxRequest) (CommandResult, error)
	ResetSandbox(context.Context, Principal, SandboxRequest) (CommandResult, error)
	RefreshSandbox(context.Context, Principal, SandboxRequest) (CommandResult, error)
}

// ConfigurationCommandService is the principal-aware configuration mutation
// capability implemented by the shared Control command executor. It remains
// separate from the Session CommandClient surface while reusing the same
// durable operation ledger.
type ConfigurationCommandService interface {
	ConfigureSessionMode(context.Context, Principal, SessionModeRequest) (CommandResult, error)
	UseSessionModel(context.Context, Principal, SessionModelRequest) (CommandResult, error)
	ConfigureSessionControllerMode(context.Context, Principal, SessionControllerModeRequest) (CommandResult, error)
	ConfigureSessionPresentationMode(context.Context, Principal, SessionPresentationModeRequest) (CommandResult, error)
	ConfigureSessionPresentation(context.Context, Principal, SessionPresentationConfigRequest) (CommandResult, error)
	ConnectModel(context.Context, Principal, ConnectModelRequest) (CommandResult, error)
	UseModel(context.Context, Principal, UseModelRequest) (CommandResult, error)
	DeleteModel(context.Context, Principal, DeleteModelRequest) (CommandResult, error)
	SetSandboxBackend(context.Context, Principal, SandboxRequest) (CommandResult, error)
	PrepareSandbox(context.Context, Principal, SandboxRequest) (CommandResult, error)
	RepairSandbox(context.Context, Principal, SandboxRequest) (CommandResult, error)
	ResetSandbox(context.Context, Principal, SandboxRequest) (CommandResult, error)
	RefreshSandbox(context.Context, Principal, SandboxRequest) (CommandResult, error)
}

type ConfigurationClient interface {
	ConfigureSessionMode(context.Context, SessionModeRequest) (CommandResult, error)
	UseSessionModel(context.Context, SessionModelRequest) (CommandResult, error)
	ConfigureSessionControllerMode(context.Context, SessionControllerModeRequest) (CommandResult, error)
	ConfigureSessionPresentationMode(context.Context, SessionPresentationModeRequest) (CommandResult, error)
	ConfigureSessionPresentation(context.Context, SessionPresentationConfigRequest) (CommandResult, error)
	ConnectModel(context.Context, ConnectModelRequest) (CommandResult, error)
	UseModel(context.Context, UseModelRequest) (CommandResult, error)
	DeleteModel(context.Context, DeleteModelRequest) (CommandResult, error)
	SetSandboxBackend(context.Context, SandboxRequest) (CommandResult, error)
	PrepareSandbox(context.Context, SandboxRequest) (CommandResult, error)
	RepairSandbox(context.Context, SandboxRequest) (CommandResult, error)
	ResetSandbox(context.Context, SandboxRequest) (CommandResult, error)
	RefreshSandbox(context.Context, SandboxRequest) (CommandResult, error)
}

type boundConfigurationClient struct {
	service   ConfigurationService
	principal Principal
}

func BindConfigurationClient(service ConfigurationService, principal Principal) (ConfigurationClient, error) {
	if service == nil {
		return nil, errors.New("controlclient: configuration service is required")
	}
	principal.ID = strings.TrimSpace(principal.ID)
	if principal.ID == "" {
		return nil, errors.New("controlclient: principal ID is required")
	}
	principal.Roles = append([]string(nil), principal.Roles...)
	return &boundConfigurationClient{service: service, principal: principal}, nil
}

func (c *boundConfigurationClient) boundPrincipal() Principal {
	principal := c.principal
	principal.Roles = append([]string(nil), principal.Roles...)
	return principal
}

func (c *boundConfigurationClient) ConfigureSessionMode(ctx context.Context, req SessionModeRequest) (CommandResult, error) {
	return c.service.ConfigureSessionMode(ctx, c.boundPrincipal(), req)
}
func (c *boundConfigurationClient) UseSessionModel(ctx context.Context, req SessionModelRequest) (CommandResult, error) {
	return c.service.UseSessionModel(ctx, c.boundPrincipal(), req)
}
func (c *boundConfigurationClient) ConfigureSessionControllerMode(ctx context.Context, req SessionControllerModeRequest) (CommandResult, error) {
	return c.service.ConfigureSessionControllerMode(ctx, c.boundPrincipal(), req)
}
func (c *boundConfigurationClient) ConfigureSessionPresentationMode(ctx context.Context, req SessionPresentationModeRequest) (CommandResult, error) {
	return c.service.ConfigureSessionPresentationMode(ctx, c.boundPrincipal(), req)
}
func (c *boundConfigurationClient) ConfigureSessionPresentation(ctx context.Context, req SessionPresentationConfigRequest) (CommandResult, error) {
	return c.service.ConfigureSessionPresentation(ctx, c.boundPrincipal(), req)
}
func (c *boundConfigurationClient) ConnectModel(ctx context.Context, req ConnectModelRequest) (CommandResult, error) {
	return c.service.ConnectModel(ctx, c.boundPrincipal(), req)
}
func (c *boundConfigurationClient) UseModel(ctx context.Context, req UseModelRequest) (CommandResult, error) {
	return c.service.UseModel(ctx, c.boundPrincipal(), req)
}
func (c *boundConfigurationClient) DeleteModel(ctx context.Context, req DeleteModelRequest) (CommandResult, error) {
	return c.service.DeleteModel(ctx, c.boundPrincipal(), req)
}
func (c *boundConfigurationClient) SetSandboxBackend(ctx context.Context, req SandboxRequest) (CommandResult, error) {
	return c.service.SetSandboxBackend(ctx, c.boundPrincipal(), req)
}
func (c *boundConfigurationClient) PrepareSandbox(ctx context.Context, req SandboxRequest) (CommandResult, error) {
	return c.service.PrepareSandbox(ctx, c.boundPrincipal(), req)
}
func (c *boundConfigurationClient) RepairSandbox(ctx context.Context, req SandboxRequest) (CommandResult, error) {
	return c.service.RepairSandbox(ctx, c.boundPrincipal(), req)
}
func (c *boundConfigurationClient) ResetSandbox(ctx context.Context, req SandboxRequest) (CommandResult, error) {
	return c.service.ResetSandbox(ctx, c.boundPrincipal(), req)
}
func (c *boundConfigurationClient) RefreshSandbox(ctx context.Context, req SandboxRequest) (CommandResult, error) {
	return c.service.RefreshSandbox(ctx, c.boundPrincipal(), req)
}

var _ ConfigurationClient = (*boundConfigurationClient)(nil)
