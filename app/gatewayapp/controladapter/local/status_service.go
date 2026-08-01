package local

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	controladapter "github.com/caelis-labs/caelis/app/gatewayapp/controladapter"
	controlclient "github.com/caelis-labs/caelis/control/client"
	controlstatus "github.com/caelis-labs/caelis/control/status"
)

// StatusService projects Session-scoped status inside the AppServer boundary.
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
	principal controlclient.Principal,
	request controlclient.StatusRequest,
) (controlstatus.StatusSnapshot, error) {
	if s == nil || s.host == nil {
		return controlstatus.StatusSnapshot{}, errors.New("app/gatewayapp/controladapter/local: status service is unavailable")
	}
	lease, err := s.host.AcquireControlRuntime(ctx, principal, controlclient.ActionSessionInspect, request.SessionID, false)
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	defer lease.Close(context.Background())
	driver, err := controladapter.NewStatusAssemblerForSession(
		ctx,
		runtimeStack(lease.Runtime()),
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

var _ controlclient.StatusService = (*StatusService)(nil)
