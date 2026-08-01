package local

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	controladapter "github.com/caelis-labs/caelis/app/gatewayapp/controladapter"
	controlclient "github.com/caelis-labs/caelis/control/client"
	controlstatus "github.com/caelis-labs/caelis/control/status"
)

// ConfigurationService is the local AppServer implementation of focused
// configuration capabilities.
type ConfigurationService struct {
	host   *gatewayapp.Stack
	status controlclient.StatusService
}

func NewConfigurationService(host *gatewayapp.Stack, status controlclient.StatusService) (*ConfigurationService, error) {
	if host == nil || status == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: host and status services are required")
	}
	return &ConfigurationService{host: host, status: status}, nil
}

func (s *ConfigurationService) ConfigureSessionMode(ctx context.Context, principal controlclient.Principal, req controlclient.SessionModeRequest) (controlstatus.StatusSnapshot, error) {
	driver, closeDriver, err := s.runtimeAdapter(ctx, principal, req.SessionID, req.Surface, false)
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	defer closeDriver()
	if req.Cycle {
		return driver.CycleSessionMode(ctx)
	}
	return driver.SetSessionMode(ctx, req.Mode)
}

func (s *ConfigurationService) ConnectModel(ctx context.Context, principal controlclient.Principal, req controlclient.ConnectModelRequest) (controlstatus.StatusSnapshot, error) {
	driver, err := s.hostAdapter(ctx, principal, req.SessionID, req.Surface)
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	if _, err := driver.Connect(ctx, req.Config); err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	return s.status.SessionStatus(ctx, principal, statusRequest(req.SessionID, req.Surface))
}

func (s *ConfigurationService) UseModel(ctx context.Context, principal controlclient.Principal, req controlclient.UseModelRequest) (controlstatus.StatusSnapshot, error) {
	active, err := s.authorizedSession(ctx, principal, req.SessionID)
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	var driver controladapter.ConfigurationAssembler
	closeDriver := func() {}
	if active.Controller.Kind == session.ControllerKindACP {
		driver, closeDriver, err = s.runtimeAdapter(ctx, principal, req.SessionID, req.Surface, true)
	} else {
		driver, err = controladapter.NewConfigurationAssemblerForSession(ctx, runtimeStack(s.host), active, req.Surface, "")
	}
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	defer closeDriver()
	if _, err := driver.UseModel(ctx, req.Model, req.ReasoningEffort); err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	return s.status.SessionStatus(ctx, principal, statusRequest(req.SessionID, req.Surface))
}

func (s *ConfigurationService) DeleteModel(ctx context.Context, principal controlclient.Principal, req controlclient.DeleteModelRequest) error {
	driver, err := s.hostAdapter(ctx, principal, req.SessionID, req.Surface)
	if err != nil {
		return err
	}
	return driver.DeleteModel(ctx, req.Model)
}

func (s *ConfigurationService) SetSandboxBackend(ctx context.Context, principal controlclient.Principal, req controlclient.SandboxRequest) (controlstatus.StatusSnapshot, error) {
	driver, err := s.hostAdapter(ctx, principal, req.SessionID, req.Surface)
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	if _, err := driver.SetSandboxBackend(ctx, req.Backend); err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	return s.status.SessionStatus(ctx, principal, statusRequest(req.SessionID, req.Surface))
}

func (s *ConfigurationService) PrepareSandbox(ctx context.Context, principal controlclient.Principal, req controlclient.SandboxRequest) (controlstatus.StatusSnapshot, error) {
	driver, err := s.hostAdapter(ctx, principal, req.SessionID, req.Surface)
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	if _, err := driver.PrepareSandbox(ctx); err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	return s.status.SessionStatus(ctx, principal, statusRequest(req.SessionID, req.Surface))
}

func (s *ConfigurationService) RepairSandbox(ctx context.Context, principal controlclient.Principal, req controlclient.SandboxRequest) (controlstatus.StatusSnapshot, error) {
	driver, err := s.hostAdapter(ctx, principal, req.SessionID, req.Surface)
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	if _, err := driver.RepairSandbox(ctx); err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	return s.status.SessionStatus(ctx, principal, statusRequest(req.SessionID, req.Surface))
}

func (s *ConfigurationService) RefreshSandbox(ctx context.Context, principal controlclient.Principal, req controlclient.SandboxRequest) error {
	if _, err := s.authorizedSession(ctx, principal, req.SessionID); err != nil {
		return err
	}
	return s.host.RefreshSandbox(ctx)
}

func (s *ConfigurationService) authorizedSession(ctx context.Context, principal controlclient.Principal, sessionID string) (session.Session, error) {
	if s == nil || s.host == nil || s.host.Sessions == nil {
		return session.Session{}, errors.New("app/gatewayapp/controladapter/local: configuration service is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if err := (controlclient.SessionAuthorizer{Sessions: s.host.Sessions}).Authorize(ctx, principal, controlclient.ActionSessionConfigure, sessionID); err != nil {
		return session.Session{}, err
	}
	return s.host.Sessions.Session(ctx, session.SessionRef{SessionID: sessionID})
}

func (s *ConfigurationService) hostAdapter(ctx context.Context, principal controlclient.Principal, sessionID, surface string) (controladapter.ConfigurationAssembler, error) {
	active, err := s.authorizedSession(ctx, principal, sessionID)
	if err != nil {
		return nil, err
	}
	return controladapter.NewConfigurationAssemblerForSession(ctx, runtimeStack(s.host), active, strings.TrimSpace(surface), "")
}

func (s *ConfigurationService) runtimeAdapter(ctx context.Context, principal controlclient.Principal, sessionID, surface string, activate bool) (controladapter.ConfigurationAssembler, func(), error) {
	lease, err := s.host.AcquireControlRuntime(ctx, principal, controlclient.ActionSessionConfigure, sessionID, activate)
	if err != nil {
		return nil, nil, err
	}
	driver, err := controladapter.NewConfigurationAssemblerForSession(ctx, runtimeStack(lease.Runtime()), lease.Session(), strings.TrimSpace(surface), "")
	if err != nil {
		_ = lease.Close(ctx)
		return nil, nil, err
	}
	return driver, func() { _ = lease.Close(context.Background()) }, nil
}

func statusRequest(sessionID, surface string) controlclient.StatusRequest {
	return controlclient.StatusRequest{SessionID: strings.TrimSpace(sessionID), Surface: strings.TrimSpace(surface), IncludeDiagnostics: true}
}

var _ controlclient.ConfigurationService = (*ConfigurationService)(nil)
