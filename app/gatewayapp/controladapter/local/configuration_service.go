package local

import (
	"context"
	"errors"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

// ConfigurationService is the local AppServer implementation of focused
// configuration capabilities.
type ConfigurationService struct {
	commands appserver.ConfigurationCommandService
}

func NewConfigurationService(host *gatewayapp.Stack) (*ConfigurationService, error) {
	if host == nil || host.ConfigurationCommands() == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: host and configuration command services are required")
	}
	return &ConfigurationService{commands: host.ConfigurationCommands()}, nil
}

func (s *ConfigurationService) ConfigureSessionMode(ctx context.Context, principal appserver.Principal, req appserver.SessionModeRequest) (appserver.CommandResult, error) {
	return s.commands.ConfigureSessionMode(ctx, principal, req)
}

func (s *ConfigurationService) UseSessionModel(ctx context.Context, principal appserver.Principal, req appserver.SessionModelRequest) (appserver.CommandResult, error) {
	return s.commands.UseSessionModel(ctx, principal, req)
}

func (s *ConfigurationService) ConfigureSessionControllerMode(ctx context.Context, principal appserver.Principal, req appserver.SessionControllerModeRequest) (appserver.CommandResult, error) {
	return s.commands.ConfigureSessionControllerMode(ctx, principal, req)
}

func (s *ConfigurationService) ConfigureSessionPresentationMode(ctx context.Context, principal appserver.Principal, req appserver.SessionPresentationModeRequest) (appserver.CommandResult, error) {
	return s.commands.ConfigureSessionPresentationMode(ctx, principal, req)
}

func (s *ConfigurationService) ConfigureSessionPresentation(ctx context.Context, principal appserver.Principal, req appserver.SessionPresentationConfigRequest) (appserver.CommandResult, error) {
	return s.commands.ConfigureSessionPresentation(ctx, principal, req)
}

func (s *ConfigurationService) ConnectModel(ctx context.Context, principal appserver.Principal, req appserver.ConnectModelRequest) (appserver.CommandResult, error) {
	return s.commands.ConnectModel(ctx, principal, req)
}

func (s *ConfigurationService) UseModel(ctx context.Context, principal appserver.Principal, req appserver.UseModelRequest) (appserver.CommandResult, error) {
	return s.commands.UseModel(ctx, principal, req)
}

func (s *ConfigurationService) DeleteModel(ctx context.Context, principal appserver.Principal, req appserver.DeleteModelRequest) (appserver.CommandResult, error) {
	return s.commands.DeleteModel(ctx, principal, req)
}

func (s *ConfigurationService) SetSandboxBackend(ctx context.Context, principal appserver.Principal, req appserver.SandboxRequest) (appserver.CommandResult, error) {
	return s.commands.SetSandboxBackend(ctx, principal, req)
}

func (s *ConfigurationService) PrepareSandbox(ctx context.Context, principal appserver.Principal, req appserver.SandboxRequest) (appserver.CommandResult, error) {
	return s.commands.PrepareSandbox(ctx, principal, req)
}

func (s *ConfigurationService) RepairSandbox(ctx context.Context, principal appserver.Principal, req appserver.SandboxRequest) (appserver.CommandResult, error) {
	return s.commands.RepairSandbox(ctx, principal, req)
}

func (s *ConfigurationService) ResetSandbox(ctx context.Context, principal appserver.Principal, req appserver.SandboxRequest) (appserver.CommandResult, error) {
	return s.commands.ResetSandbox(ctx, principal, req)
}

func (s *ConfigurationService) RefreshSandbox(ctx context.Context, principal appserver.Principal, req appserver.SandboxRequest) (appserver.CommandResult, error) {
	return s.commands.RefreshSandbox(ctx, principal, req)
}

var _ appserver.ConfigurationService = (*ConfigurationService)(nil)
