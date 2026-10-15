package appserver

import (
	"context"

	"github.com/caelis-labs/caelis/control/sessionprofile"
)

// ApplicationReviewerState reads the creation-bound review route for the exact
// authenticated application connection. It reports local readiness only; it
// never activates a Session Runtime or contacts the reviewer provider.
func (s *SessionProfileService) SessionReviewerState(ctx context.Context, p Principal, sessionID string) (sessionprofile.ReviewerState, error) {
	scope, err := SessionScope(p)
	if err != nil {
		return sessionprofile.ReviewerState{}, err
	}
	binding, err := s.config.Store.GetBinding(ctx, scope, sessionID)
	if err != nil {
		return sessionprofile.ReviewerState{}, err
	}
	if s.config.ReviewerState == nil {
		return sessionprofile.ReviewerState{}, sessionprofile.ErrUnsupported
	}
	state, err := s.config.ReviewerState(ctx, binding.Profile)
	if err != nil {
		return sessionprofile.ReviewerState{}, err
	}
	state.SessionID = binding.SessionID
	return state, nil
}
