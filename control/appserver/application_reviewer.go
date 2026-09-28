package appserver

import (
	"context"

	"github.com/caelis-labs/caelis/control/application"
)

// ApplicationReviewerState reads the creation-bound review route for the exact
// authenticated application connection. It reports local readiness only; it
// never activates a Session Runtime or contacts the reviewer provider.
func (s *ApplicationService) ApplicationReviewerState(ctx context.Context, p Principal, sessionID string) (application.ReviewerState, error) {
	scope, err := ApplicationScope(p)
	if err != nil {
		return application.ReviewerState{}, err
	}
	binding, err := s.config.Store.GetBinding(ctx, scope, sessionID)
	if err != nil {
		return application.ReviewerState{}, err
	}
	if s.config.ReviewerState == nil {
		return application.ReviewerState{}, application.ErrUnsupported
	}
	state, err := s.config.ReviewerState(ctx, binding.Profile)
	if err != nil {
		return application.ReviewerState{}, err
	}
	state.SessionID = binding.SessionID
	return state, nil
}
