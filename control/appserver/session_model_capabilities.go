package appserver

import (
	"context"

	"github.com/caelis-labs/caelis/control/sessionprofile"
)

// SessionModelCapabilities reads only the selected Session's
// desired model. No worker, global default or union capability is substituted.
func (s *SessionProfileService) SessionModelCapabilities(ctx context.Context, p Principal, sessionID string) (sessionprofile.ModelCapabilities, error) {
	configuration, err := s.SessionConfiguration(ctx, p, sessionID)
	if err != nil {
		return sessionprofile.ModelCapabilities{}, err
	}
	if s.config.ModelImageInput == nil {
		return sessionprofile.ModelCapabilities{}, sessionprofile.ErrUnsupported
	}
	imageInput, err := s.config.ModelImageInput(ctx, configuration.Profile)
	if err != nil {
		return sessionprofile.ModelCapabilities{}, err
	}
	return sessionprofile.ModelCapabilities{SessionID: configuration.SessionID, ConfigurationRevision: configuration.Revision, Model: configuration.Profile.Model, ImageInput: imageInput}, nil
}
