package controlclient

import (
	"context"
	"errors"
	"strings"

	controlstatus "github.com/caelis-labs/caelis/control/status"
)

// StatusRequest addresses one Session-scoped product status projection.
// IncludeDiagnostics selects the full /status and /doctor view instead of the
// lightweight prompt-bar projection.
type StatusRequest struct {
	SessionID          string `json:"session_id"`
	Surface            string `json:"surface,omitempty"`
	IncludeDiagnostics bool   `json:"include_diagnostics,omitempty"`
}

// StatusService is the principal-aware AppServer status capability.
type StatusService interface {
	SessionStatus(context.Context, Principal, StatusRequest) (controlstatus.StatusSnapshot, error)
}

// StatusClient is the principal-bound status capability consumed by surfaces.
type StatusClient interface {
	SessionStatus(context.Context, StatusRequest) (controlstatus.StatusSnapshot, error)
}

type boundStatusClient struct {
	service   StatusService
	principal Principal
}

// BindStatusClient binds one trusted principal to the status capability.
func BindStatusClient(service StatusService, principal Principal) (StatusClient, error) {
	if service == nil {
		return nil, errors.New("controlclient: status service is required")
	}
	principal.ID = strings.TrimSpace(principal.ID)
	if principal.ID == "" {
		return nil, errors.New("controlclient: principal ID is required")
	}
	principal.Roles = append([]string(nil), principal.Roles...)
	return &boundStatusClient{service: service, principal: principal}, nil
}

func (c *boundStatusClient) SessionStatus(ctx context.Context, request StatusRequest) (controlstatus.StatusSnapshot, error) {
	principal := c.principal
	principal.Roles = append([]string(nil), principal.Roles...)
	return c.service.SessionStatus(ctx, principal, request)
}

var _ StatusClient = (*boundStatusClient)(nil)
