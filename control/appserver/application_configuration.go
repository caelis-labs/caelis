package appserver

import (
	"context"
	"strconv"

	"github.com/caelis-labs/caelis/control/application"
)

// Capabilities reports implemented application features, independent of the
// caller's lease, per-Session permissions and current execution state.
func (s *ApplicationService) Capabilities() []string {
	out := []string{
		application.Capability, application.CapabilityResourceTransfer,
		application.CapabilityToolResultContent, application.CapabilityMediaResources,
		application.CapabilityBackgroundActivation, CapabilitySharedWorkers, CapabilityTurnSteering,
		CapabilityExecutionConfiguration,
	}
	if s.config.ModelImageInput != nil {
		out = append(out, application.CapabilityModelCapabilities)
	}
	if s.config.ReviewerState != nil {
		out = append(out, application.CapabilityGuardianReview)
	}
	if s.config.ValidateProfile != nil {
		out = append(out, application.CapabilityHotConfiguration)
		if s.config.MCPStatus != nil {
			out = append(out, application.CapabilityAtomicCapabilities)
		}
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

// ApplicationMCPStatus reads this connection's desired MCP and Skill selections
// with optional resident health. Reading it never starts services or scans files.
func (s *ApplicationService) ApplicationMCPStatus(ctx context.Context, p Principal, sessionID string) (application.MCPStatus, error) {
	scope, err := ApplicationScope(p)
	if err != nil {
		return application.MCPStatus{}, err
	}
	if s.config.MCPStatus == nil {
		return application.MCPStatus{}, application.ErrUnsupported
	}
	configuration, err := s.config.Store.Configuration(ctx, scope, sessionID)
	if err != nil {
		return application.MCPStatus{}, err
	}
	observed := s.config.MCPStatus(ctx, sessionID, configuration.Revision)
	byName := make(map[string]application.MCPServerStatus, len(observed.Servers))
	for _, server := range observed.Servers {
		byName[server.Name] = server
	}
	status := application.MCPStatus{SessionID: sessionID, ConfigurationRevision: strconv.FormatUint(configuration.Revision, 10), Servers: make([]application.MCPServerStatus, 0, len(configuration.Profile.MCPServers))}
	for _, declared := range configuration.Profile.MCPServers {
		server, ok := byName[declared.Name]
		if !ok {
			server = application.MCPServerStatus{Name: declared.Name, Status: "inactive"}
		}
		status.Servers = append(status.Servers, server)
	}
	status.Skills = make([]application.SkillStatus, 0, len(configuration.Profile.SkillDirs)+len(configuration.Profile.SkillRoots))
	status.Skills = append(status.Skills, observed.Skills...)
	if len(status.Skills) == 0 {
		for _, path := range configuration.Profile.SkillDirs {
			status.Skills = append(status.Skills, application.SkillStatus{Path: path, Kind: "directory", Status: "inactive"})
		}
		for _, path := range configuration.Profile.SkillRoots {
			status.Skills = append(status.Skills, application.SkillStatus{Path: path, Kind: "skill", Status: "inactive"})
		}
	}
	return status, nil
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
	if s.config.ReviewerState == nil && req.Patch.Tools != nil && requiresApplicationCallbackApproval(*req.Patch.Tools) {
		return application.Configuration{}, application.ErrUnsupported
	}
	updated, err := s.config.Store.UpdateConfiguration(ctx, scope, sessionID, req, s.config.ValidateProfile)
	if err == nil && s.config.ConfigurationCommitted != nil {
		s.config.ConfigurationCommitted(ctx, sessionID, updated)
	}
	return updated, err
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
