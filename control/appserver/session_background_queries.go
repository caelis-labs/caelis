package appserver

import (
	"context"

	"github.com/caelis-labs/caelis/control/sessionprofile"
)

// BackgroundGrant reads one exact-Session grant in the authenticated scope.
func (s *SessionProfileService) BackgroundGrant(ctx context.Context, p Principal, sessionID, grantID string) (sessionprofile.BackgroundGrant, error) {
	scope, err := SessionScope(p)
	if err != nil {
		return sessionprofile.BackgroundGrant{}, err
	}
	return s.config.Store.GetBackgroundGrant(ctx, scope, sessionID, grantID)
}

// ListBackgroundGrants includes revoked grants for durable recovery.
func (s *SessionProfileService) ListBackgroundGrants(ctx context.Context, p Principal, sessionID string) ([]sessionprofile.BackgroundGrant, error) {
	scope, err := SessionScope(p)
	if err != nil {
		return nil, err
	}
	return s.config.Store.ListBackgroundGrants(ctx, scope, sessionID)
}
