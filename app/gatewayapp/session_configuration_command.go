package gatewayapp

import (
	"context"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func (s *controlCommandBackend) updateSessionConfiguration(ctx context.Context, p appserver.Principal, req appserver.UpdateSessionConfigurationRequest) (appserver.CommandResult, error) {
	scope, err := appserver.SessionScope(p)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	configuration, err := s.composition.authorities.sessionProfiles.UpdateConfiguration(ctx, scope, req.SessionID,
		sessionprofile.UpdateConfigurationRequest{OperationID: req.OperationID, ExpectedConfigurationRevision: req.ExpectedConfigurationRevision, Patch: req.Patch},
		s.composition.validateApplicationProfile)
	if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	return appserver.CommandResult{OperationID: req.OperationID, SessionID: req.SessionID, Outcome: appserver.OutcomeCommitted, Configuration: &configuration}, nil
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
