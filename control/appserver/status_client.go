package appserver

import (
	"context"
	"errors"
	"strings"

	controlstatus "github.com/caelis-labs/caelis/control/status"
)

// StatusRequest addresses product status. SessionID is optional context for a
// selected Session; an empty value returns the explicitly addressed workspace
// projection. WorkspaceKey and CWD prevent a persistent multi-workspace Host
// from substituting the workspace that happened to start the Host.
// IncludeDiagnostics selects the full /status and /doctor view instead of the
// lightweight prompt-bar projection.
type StatusRequest struct {
	SessionID          string `json:"session_id,omitempty"`
	WorkspaceKey       string `json:"workspace_key,omitempty"`
	CWD                string `json:"cwd,omitempty"`
	Surface            string `json:"surface,omitempty"`
	IncludeDiagnostics bool   `json:"include_diagnostics,omitempty"`
	// IncludeWorkspaceTrustRequirement requests the project-MCP presence
	// projection used only by the interactive workspace trust preflight.
	IncludeWorkspaceTrustRequirement bool `json:"include_workspace_trust_requirement,omitempty"`
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
