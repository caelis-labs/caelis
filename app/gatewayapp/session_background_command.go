package gatewayapp

import (
	"context"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func (s *controlCommandBackend) createSessionBackgroundGrant(ctx context.Context, p appserver.Principal, req appserver.CreateSessionBackgroundGrantRequest) (appserver.CommandResult, error) {
	scope, err := appserver.SessionScope(p)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	grant, err := s.composition.authorities.sessionProfiles.CreateBackgroundGrant(ctx, scope, req.SessionID, sessionprofile.BackgroundGrantRequest{OperationID: req.OperationID, Source: req.Source, AuthorizationOperationID: req.AuthorizationOperationID})
	if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	return appserver.CommandResult{OperationID: req.OperationID, SessionID: req.SessionID, Outcome: appserver.OutcomeCommitted, BackgroundGrant: &grant}, nil
}
func (s *controlCommandBackend) revokeSessionBackgroundGrant(ctx context.Context, p appserver.Principal, req appserver.RevokeSessionBackgroundGrantRequest) (appserver.CommandResult, error) {
	scope, err := appserver.SessionScope(p)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	grant, err := s.composition.authorities.sessionProfiles.RevokeBackgroundGrant(ctx, scope, req.SessionID, req.GrantID)
	if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	return appserver.CommandResult{OperationID: req.OperationID, SessionID: req.SessionID, Outcome: appserver.OutcomeCommitted, BackgroundGrant: &grant}, nil
}
