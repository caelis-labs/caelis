package gatewayapp

import (
	"context"
	"errors"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func (s *controlCommandBackend) updateSessionConfiguration(ctx context.Context, p appserver.Principal, req appserver.UpdateSessionConfigurationRequest) (appserver.CommandResult, error) {
	scope, err := appserver.SessionScope(p)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	if _, err := s.composition.authorities.sessionProfiles.BindingForSession(ctx, req.SessionID); errors.Is(err, sessionprofile.ErrNotFound) {
		return s.updateNativePluginConfiguration(ctx, scope, req)
	} else if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	configuration, err := s.composition.authorities.sessionProfiles.UpdateConfiguration(ctx, scope, req.SessionID,
		sessionprofile.UpdateConfigurationRequest{OperationID: req.OperationID, ExpectedConfigurationRevision: req.ExpectedConfigurationRevision, Patch: req.Patch},
		s.composition.validateApplicationProfile)
	if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	if configuration.Revision > req.ExpectedConfigurationRevision &&
		(req.Patch.MCPServers != nil && len(configuration.Profile.MCPServers) != 0 ||
			req.Patch.Plugins != nil && len(configuration.Profile.Plugins) != 0) {
		s.prepareObservedMCP(ctx, req.SessionID)
	}
	return appserver.CommandResult{OperationID: req.OperationID, SessionID: req.SessionID, Outcome: appserver.OutcomeCommitted, Configuration: &configuration}, nil
}

// A subscribed Application can prepare MCP before its first prompt. The
// configuration receipt has already committed and Runtime health is separate.
func (s *controlCommandBackend) prepareObservedMCP(ctx context.Context, sessionID string) {
	registry := s.runtimeRegistry()
	if registry == nil {
		return
	}
	registry.mu.RLock()
	cold := registry.observers[sessionID] != 0 && registry.sessions[sessionID] == nil
	registry.mu.RUnlock()
	if cold {
		_, _, release, _, err := registry.acquireActivatedControlRuntime(ctx, sessionID)
		if err == nil {
			_ = release(ctx)
		}
	}
}

func (s *controlCommandBackend) createSessionResource(ctx context.Context, p appserver.Principal, req appserver.SessionResourceRequest) (appserver.CommandResult, error) {
	scope, err := appserver.SessionScope(p)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	resource, err := s.composition.authorities.sessionProfiles.CreateResourceWithExpiry(ctx, scope, req.SessionID, req.OperationID, req.Name, req.MediaType, req.Data, req.ExpiresAt)
	if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	return appserver.CommandResult{OperationID: req.OperationID, SessionID: req.SessionID, Outcome: appserver.OutcomeCommitted, SessionResource: &resource}, nil
}
