package gatewayapp

import (
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// WorkspaceReadService is the focused Host-private authority for canonical
// workspace addressing and future-Session discovery reads.
type WorkspaceReadService struct {
	composition *runtimeComposition
	registry    *sessionRuntimeRegistry
}

// WorkspaceReads returns the focused workspace read authority used by
// AppServer status and completion services.
func (s *Stack) WorkspaceReads() WorkspaceReadService {
	if s == nil {
		return WorkspaceReadService{}
	}
	return WorkspaceReadService{composition: &s.composition, registry: s.sessionRuntimes}
}

// ResolveWorkspaceAddress canonicalizes a client workspace address and
// rejects aliases that conflict with workspaces already known by this Host.
// Read-only projections use this without retaining a Session Runtime or
// registering another lifecycle authority.
func (s *Stack) ResolveWorkspaceAddress(requested session.WorkspaceRef) (session.WorkspaceRef, error) {
	return s.WorkspaceReads().Resolve(requested)
}

// Resolve canonicalizes one client workspace address and validates that it
// does not conflict with the Host registry's durable identity map.
func (s WorkspaceReadService) Resolve(requested session.WorkspaceRef) (session.WorkspaceRef, error) {
	if s.composition == nil || s.registry == nil {
		return session.WorkspaceRef{}, sessionRuntimeHostClosingError()
	}
	workspace, err := canonicalWorkspaceRef(requested, s.composition.workspace)
	if err != nil {
		return session.WorkspaceRef{}, err
	}
	if err := s.registry.validateWorkspaceIdentity(workspace); err != nil {
		return session.WorkspaceRef{}, err
	}
	return workspace, nil
}
