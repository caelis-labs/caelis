package controlclient

import (
	"context"
	"errors"
	"strings"

	controlstatus "github.com/caelis-labs/caelis/control/status"
)

type SessionModeRequest struct {
	SessionID string `json:"session_id"`
	Mode      string `json:"mode,omitempty"`
	Cycle     bool   `json:"cycle,omitempty"`
	Surface   string `json:"surface,omitempty"`
}

type ConnectModelRequest struct {
	SessionID string        `json:"session_id"`
	Surface   string        `json:"surface,omitempty"`
	Config    ConnectConfig `json:"config"`
}

type UseModelRequest struct {
	SessionID       string `json:"session_id"`
	Surface         string `json:"surface,omitempty"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type DeleteModelRequest struct {
	SessionID string `json:"session_id"`
	Surface   string `json:"surface,omitempty"`
	Model     string `json:"model"`
}

type SandboxRequest struct {
	SessionID string `json:"session_id"`
	Surface   string `json:"surface,omitempty"`
	Backend   string `json:"backend,omitempty"`
}

// ConfigurationService owns typed AppServer configuration operations. It is a
// transport contract; model, sandbox, and Session policy semantics remain with
// their existing Control owners.
type ConfigurationService interface {
	ConfigureSessionMode(context.Context, Principal, SessionModeRequest) (controlstatus.StatusSnapshot, error)
	ConnectModel(context.Context, Principal, ConnectModelRequest) (controlstatus.StatusSnapshot, error)
	UseModel(context.Context, Principal, UseModelRequest) (controlstatus.StatusSnapshot, error)
	DeleteModel(context.Context, Principal, DeleteModelRequest) error
	SetSandboxBackend(context.Context, Principal, SandboxRequest) (controlstatus.StatusSnapshot, error)
	PrepareSandbox(context.Context, Principal, SandboxRequest) (controlstatus.StatusSnapshot, error)
	RepairSandbox(context.Context, Principal, SandboxRequest) (controlstatus.StatusSnapshot, error)
	RefreshSandbox(context.Context, Principal, SandboxRequest) error
}

type ConfigurationClient interface {
	ConfigureSessionMode(context.Context, SessionModeRequest) (controlstatus.StatusSnapshot, error)
	ConnectModel(context.Context, ConnectModelRequest) (controlstatus.StatusSnapshot, error)
	UseModel(context.Context, UseModelRequest) (controlstatus.StatusSnapshot, error)
	DeleteModel(context.Context, DeleteModelRequest) error
	SetSandboxBackend(context.Context, SandboxRequest) (controlstatus.StatusSnapshot, error)
	PrepareSandbox(context.Context, SandboxRequest) (controlstatus.StatusSnapshot, error)
	RepairSandbox(context.Context, SandboxRequest) (controlstatus.StatusSnapshot, error)
	RefreshSandbox(context.Context, SandboxRequest) error
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

func (c *boundConfigurationClient) ConfigureSessionMode(ctx context.Context, req SessionModeRequest) (controlstatus.StatusSnapshot, error) {
	return c.service.ConfigureSessionMode(ctx, c.boundPrincipal(), req)
}
func (c *boundConfigurationClient) ConnectModel(ctx context.Context, req ConnectModelRequest) (controlstatus.StatusSnapshot, error) {
	return c.service.ConnectModel(ctx, c.boundPrincipal(), req)
}
func (c *boundConfigurationClient) UseModel(ctx context.Context, req UseModelRequest) (controlstatus.StatusSnapshot, error) {
	return c.service.UseModel(ctx, c.boundPrincipal(), req)
}
func (c *boundConfigurationClient) DeleteModel(ctx context.Context, req DeleteModelRequest) error {
	return c.service.DeleteModel(ctx, c.boundPrincipal(), req)
}
func (c *boundConfigurationClient) SetSandboxBackend(ctx context.Context, req SandboxRequest) (controlstatus.StatusSnapshot, error) {
	return c.service.SetSandboxBackend(ctx, c.boundPrincipal(), req)
}
func (c *boundConfigurationClient) PrepareSandbox(ctx context.Context, req SandboxRequest) (controlstatus.StatusSnapshot, error) {
	return c.service.PrepareSandbox(ctx, c.boundPrincipal(), req)
}
func (c *boundConfigurationClient) RepairSandbox(ctx context.Context, req SandboxRequest) (controlstatus.StatusSnapshot, error) {
	return c.service.RepairSandbox(ctx, c.boundPrincipal(), req)
}
func (c *boundConfigurationClient) RefreshSandbox(ctx context.Context, req SandboxRequest) error {
	return c.service.RefreshSandbox(ctx, c.boundPrincipal(), req)
}

var _ ConfigurationClient = (*boundConfigurationClient)(nil)
