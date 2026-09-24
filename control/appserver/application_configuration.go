package appserver

import (
	"context"

	"github.com/caelis-labs/caelis/control/application"
)

// Capabilities reports implemented application features, independent of the
// caller's lease, per-Session permissions and current execution state.
func (s *ApplicationService) Capabilities() []string {
	out := []string{application.Capability, application.CapabilityResourceTransfer, application.CapabilityBackgroundActivation, CapabilitySharedWorkers, CapabilityTurnSteering}
	if s.config.ValidateProfile != nil {
		out = append(out, application.CapabilityHotConfiguration)
	}
	if s.config.NativeExecution {
		out = append(out, application.CapabilityNativeExecution, application.CapabilityWorkspaceBinding)
	}
	return out
}

// ApplicationConfiguration reads the desired profile and latest admitted model
// request. Its revision is independent of canonical Session history revisions.
func (s *ApplicationService) ApplicationConfiguration(ctx context.Context, p Principal, sessionID string) (application.Configuration, error) {
	scope, err := ApplicationScope(p)
	if err != nil {
		return application.Configuration{}, err
	}
	return s.config.Store.Configuration(ctx, scope, sessionID)
}

// UpdateApplicationConfiguration commits a validated profile without submitting
// input or waiting for an active Turn. The Store serializes commit with model
// request admission; an issued request retains its complete previous snapshot.
func (s *ApplicationService) UpdateApplicationConfiguration(ctx context.Context, p Principal, sessionID string, req application.UpdateConfigurationRequest) (application.Configuration, error) {
	scope, err := ApplicationScope(p)
	if err != nil {
		return application.Configuration{}, err
	}
	if s.config.ValidateProfile == nil {
		return application.Configuration{}, application.ErrUnsupported
	}
	return s.config.Store.UpdateConfiguration(ctx, scope, sessionID, req, s.config.ValidateProfile)
}

// ApplicationConfigurationOperation returns the original atomic update receipt,
// including after a lost response or Host restart. It never dispatches a Turn.
func (s *ApplicationService) ApplicationConfigurationOperation(ctx context.Context, p Principal, operationID string) (application.Configuration, error) {
	scope, err := ApplicationScope(p)
	if err != nil {
		return application.Configuration{}, err
	}
	return s.config.Store.ConfigurationOperation(ctx, scope, operationID)
}
