package appserver

import (
	"context"

	"github.com/caelis-labs/caelis/control/application"
)

// CreateBackgroundGrant records the embedding application's prior user
// authorization for one existing Session. The source is descriptive; this
// operation never schedules work or admits a prompt by itself.
func (s *ApplicationService) CreateBackgroundGrant(ctx context.Context, p Principal, sessionID string, req application.BackgroundGrantRequest) (application.BackgroundGrant, error) {
	scope, err := ApplicationScope(p)
	if err != nil {
		return application.BackgroundGrant{}, err
	}
	return s.config.Store.CreateBackgroundGrant(ctx, scope, sessionID, req)
}

// BackgroundGrant reads one exact-Session grant in the authenticated scope.
func (s *ApplicationService) BackgroundGrant(ctx context.Context, p Principal, sessionID, grantID string) (application.BackgroundGrant, error) {
	scope, err := ApplicationScope(p)
	if err != nil {
		return application.BackgroundGrant{}, err
	}
	return s.config.Store.GetBackgroundGrant(ctx, scope, sessionID, grantID)
}

// ListBackgroundGrants includes revoked grants for durable recovery.
func (s *ApplicationService) ListBackgroundGrants(ctx context.Context, p Principal, sessionID string) ([]application.BackgroundGrant, error) {
	scope, err := ApplicationScope(p)
	if err != nil {
		return nil, err
	}
	return s.config.Store.ListBackgroundGrants(ctx, scope, sessionID)
}

// RevokeBackgroundGrant blocks subsequent prompt admissions without stopping
// already accepted work or changing the application connection's lease.
func (s *ApplicationService) RevokeBackgroundGrant(ctx context.Context, p Principal, sessionID, grantID string) (application.BackgroundGrant, error) {
	scope, err := ApplicationScope(p)
	if err != nil {
		return application.BackgroundGrant{}, err
	}
	return s.config.Store.RevokeBackgroundGrant(ctx, scope, sessionID, grantID)
}
