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
	hostDeps                  *controladapter.StatusAssemblyDeps
	acquireRuntime            acquireControlRuntimeFunc
	resolveWorkspace          resolveWorkspaceAddressFunc
	sandboxStatusForWorkspace func(session.WorkspaceRef) gatewayapp.SandboxStatus
	doctorForWorkspace        func(context.Context, session.WorkspaceRef, gatewayapp.DoctorRequest) (gatewayapp.DoctorReport, error)
}

type statusServiceDeps struct {
	hostDeps                  *controladapter.StatusAssemblyDeps
	acquireRuntime            acquireControlRuntimeFunc
	resolveWorkspace          resolveWorkspaceAddressFunc
	sandboxStatusForWorkspace func(session.WorkspaceRef) gatewayapp.SandboxStatus
	doctorForWorkspace        func(context.Context, session.WorkspaceRef, gatewayapp.DoctorRequest) (gatewayapp.DoctorReport, error)
}

func newStatusService(deps statusServiceDeps) (*StatusService, error) {
	if deps.hostDeps == nil || deps.acquireRuntime == nil || deps.resolveWorkspace == nil ||
		deps.sandboxStatusForWorkspace == nil || deps.doctorForWorkspace == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: status service dependencies are required")
	}
	return &StatusService{
		hostDeps: deps.hostDeps, acquireRuntime: deps.acquireRuntime, resolveWorkspace: deps.resolveWorkspace,
		sandboxStatusForWorkspace: deps.sandboxStatusForWorkspace, doctorForWorkspace: deps.doctorForWorkspace,
	}, nil
}

func (s *StatusService) SessionStatus(
	ctx context.Context,
	principal appserver.Principal,
	request appserver.StatusRequest,
) (result controlstatus.StatusSnapshot, returnErr error) {
	if s == nil || s.hostDeps == nil {
		return controlstatus.StatusSnapshot{}, errors.New("app/gatewayapp/controladapter/local: status service is unavailable")
	}
	if err := authorizeHostCapability(principal); err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	if strings.TrimSpace(request.SessionID) == "" {
		workspace, err := s.resolveWorkspace(session.WorkspaceRef{
			Key: strings.TrimSpace(request.WorkspaceKey),
			CWD: strings.TrimSpace(request.CWD),
		})
		if err != nil {
			return controlstatus.StatusSnapshot{}, err
		}
		deps := *s.hostDeps
		deps.Session.Workspace = workspace
		deps.Sandbox.StatusFn = func() SandboxStatusProjection {
			return toSandboxStatusProjection(s.sandboxStatusForWorkspace(workspace))
		}
		deps.Status.DoctorFn = func(ctx context.Context, req DoctorRequest) (DoctorStatusProjection, error) {
			return toDoctorStatusProjection(s.doctorForWorkspace(ctx, workspace, req))
		}
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
	lease, err := s.acquireRuntime(ctx, principal, appserver.ActionSessionInspect, request.SessionID, false)
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, lease.Close(context.Background()))
	}()
	deps := statusAssemblyDepsFromLease(lease)
	if deps == nil {
		return controlstatus.StatusSnapshot{}, errors.New("app/gatewayapp/controladapter/local: status Runtime projection is unavailable")
	}
	driver, err := controladapter.NewStatusAssemblerForSession(
		ctx,
		*deps,
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
