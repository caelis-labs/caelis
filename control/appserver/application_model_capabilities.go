package appserver

import (
	"context"

	"github.com/caelis-labs/caelis/control/application"
)

// ApplicationModelCapabilities reads only the exact application-owned Session's
// desired model. No worker, global default or union capability is substituted.
func (s *ApplicationService) ApplicationModelCapabilities(ctx context.Context, p Principal, sessionID string) (application.ModelCapabilities, error) {
	configuration, err := s.ApplicationConfiguration(ctx, p, sessionID)
	if err != nil {
		return application.ModelCapabilities{}, err
	}
	if s.config.ModelImageInput == nil {
		return application.ModelCapabilities{}, application.ErrUnsupported
	}
	imageInput, err := s.config.ModelImageInput(ctx, configuration.Profile)
	if err != nil {
		return application.ModelCapabilities{}, err
	}
	return application.ModelCapabilities{SessionID: configuration.SessionID, ConfigurationRevision: configuration.Revision, Model: configuration.Profile.Model, ImageInput: imageInput}, nil
}
