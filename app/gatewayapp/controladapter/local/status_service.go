package local

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	controladapter "github.com/caelis-labs/caelis/app/gatewayapp/controladapter"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	controlstatus "github.com/caelis-labs/caelis/control/status"
)

// StatusService projects Host/workspace status and optional selected-Session
// context inside the AppServer boundary.
// It reuses the maintained status assembler while keeping Runtime handles out
// of presentation surfaces.
type StatusService struct {
	host *gatewayapp.Stack
}

// NewStatusService constructs the focused AppServer status capability.
func NewStatusService(host *gatewayapp.Stack) (*StatusService, error) {
	if host == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: host Stack is required")
	}
	return &StatusService{host: host}, nil
}

func (s *StatusService) SessionStatus(
	ctx context.Context,
	principal appserver.Principal,
	request appserver.StatusRequest,
) (result controlstatus.StatusSnapshot, returnErr error) {
	if s == nil || s.host == nil {
		return controlstatus.StatusSnapshot{}, errors.New("app/gatewayapp/controladapter/local: status service is unavailable")
	}
	if err := authorizeHostCapability(principal); err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	if strings.TrimSpace(request.SessionID) == "" {
		workspace, err := s.host.ResolveWorkspaceAddress(session.WorkspaceRef{
			Key: strings.TrimSpace(request.WorkspaceKey),
			CWD: strings.TrimSpace(request.CWD),
		})
		if err != nil {
			return controlstatus.StatusSnapshot{}, err
		}
		deps := controlRuntimeDepsForWorkspace(s.host, workspace)
		driver := controladapter.NewStatusAssemblerForHost(
			deps,
			strings.TrimSpace(request.Surface),
			"",
		)
		if request.IncludeDiagnostics {
			return driver.Status(ctx)
		}
		return driver.LightweightStatus(ctx)
	}
	lease, err := s.host.AcquireControlRuntime(ctx, principal, appserver.ActionSessionInspect, request.SessionID, false)
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, lease.Close(context.Background()))
	}()
	driver, err := controladapter.NewStatusAssemblerForSession(
		ctx,
		controlRuntimeDepsFromView(lease.ControlRuntimeView()),
		lease.Session(),
		strings.TrimSpace(request.Surface),
		"",
	)
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	if request.IncludeDiagnostics {
		return driver.Status(ctx)
	}
	return driver.LightweightStatus(ctx)
}

var _ appserver.StatusService = (*StatusService)(nil)
