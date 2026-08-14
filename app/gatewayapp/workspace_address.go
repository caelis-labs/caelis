package gatewayapp

import "github.com/caelis-labs/caelis/agent-sdk/session"

// ResolveWorkspaceAddress canonicalizes a client workspace address and
// rejects aliases that conflict with workspaces already known by this Host.
// Read-only projections use this without retaining a Session Runtime or
// registering another lifecycle authority.
func (s *Stack) ResolveWorkspaceAddress(requested session.WorkspaceRef) (session.WorkspaceRef, error) {
	if s == nil || s.sessionRuntimes == nil {
		return session.WorkspaceRef{}, sessionRuntimeHostClosingError()
	}
	workspace, err := canonicalWorkspaceRef(requested, s.Workspace)
	if err != nil {
		return session.WorkspaceRef{}, err
	}
	if err := s.sessionRuntimes.validateWorkspaceIdentity(workspace); err != nil {
		return session.WorkspaceRef{}, err
	}
	return workspace, nil
}
