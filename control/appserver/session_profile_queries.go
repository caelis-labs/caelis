package appserver

import (
	"context"
	"strconv"

	"github.com/caelis-labs/caelis/control/sessionprofile"
)

// CapabilitySessionProfileControl exposes profile-aware status,
// presentation, native Agent state/catalog, model/Skill completion, fenced
// Session compaction and canonical closure.
const CapabilitySessionProfileControl = "session-profile-control-v1"

// CapabilitySessionProfileChildren exposes native participant and Spawn
// execution with independent Sessions inheriting the admitted explicit profile.
const CapabilitySessionProfileChildren = "session-profile-children-v1"

// Capabilities reports implemented profile features, independent of per-Session
// permissions and current execution state.
func (s *SessionProfileService) Capabilities() []string {
	out := []string{
		sessionprofile.Capability, sessionprofile.CapabilityResourceTransfer,
		sessionprofile.CapabilityToolResultContent, sessionprofile.CapabilityMediaResources,
		sessionprofile.CapabilityBackgroundActivation, CapabilityTurnSteering, CapabilitySessionProfileControl,
		CapabilityExecutionConfiguration,
	}
	if s.config.ModelImageInput != nil {
		out = append(out, sessionprofile.CapabilityModelCapabilities)
	}
	if s.config.ReviewerState != nil {
		out = append(out, sessionprofile.CapabilityGuardianReview, sessionprofile.CapabilityRuntimeAgentTeam)
	}
	if s.config.ValidateProfile != nil {
		out = append(out, sessionprofile.CapabilityHotConfiguration)
		if s.config.MCPStatus != nil {
			out = append(out, sessionprofile.CapabilityAtomicCapabilities)
		}
	}
	if s.config.NativeExecution {
		out = append(out, sessionprofile.CapabilityNativeExecution, sessionprofile.CapabilityWorkspaceBinding)
	}
	if s.config.NativeProfileChildren {
		out = append(out, CapabilitySessionProfileChildren)
	}
	return out
}

// SessionConfiguration reads the desired profile and latest admitted model
// request. Its revision is independent of canonical Session history revisions.
func (s *SessionProfileService) SessionConfiguration(ctx context.Context, p Principal, sessionID string) (sessionprofile.Configuration, error) {
	scope, err := SessionScope(p)
	if err != nil {
		return sessionprofile.Configuration{}, err
	}
	return s.config.Store.Configuration(ctx, scope, sessionID)
}

// SessionMCPStatus reads the Session's desired MCP and Skill selections
// with optional resident health. Reading it never starts services or scans files.
func (s *SessionProfileService) SessionMCPStatus(ctx context.Context, p Principal, sessionID string) (sessionprofile.MCPStatus, error) {
	scope, err := SessionScope(p)
	if err != nil {
		return sessionprofile.MCPStatus{}, err
	}
	if s.config.MCPStatus == nil {
		return sessionprofile.MCPStatus{}, sessionprofile.ErrUnsupported
	}
	configuration, err := s.config.Store.Configuration(ctx, scope, sessionID)
	if err != nil {
		return sessionprofile.MCPStatus{}, err
	}
	observed := s.config.MCPStatus(ctx, sessionID, configuration.Revision)
	byName := make(map[string]sessionprofile.MCPServerStatus, len(observed.Servers))
	for _, server := range observed.Servers {
		byName[server.Name] = server
	}
	status := sessionprofile.MCPStatus{SessionID: sessionID, ConfigurationRevision: strconv.FormatUint(configuration.Revision, 10), Servers: make([]sessionprofile.MCPServerStatus, 0, len(configuration.Profile.MCPServers))}
	for _, declared := range configuration.Profile.MCPServers {
		server, ok := byName[declared.Name]
		if !ok {
			server = sessionprofile.MCPServerStatus{Name: declared.Name, Status: "inactive"}
		}
		status.Servers = append(status.Servers, server)
	}
	status.Skills = make([]sessionprofile.SkillStatus, 0, len(configuration.Profile.SkillDirs)+len(configuration.Profile.SkillRoots))
	status.Skills = append(status.Skills, observed.Skills...)
	if len(status.Skills) == 0 {
		for _, path := range configuration.Profile.SkillDirs {
			status.Skills = append(status.Skills, sessionprofile.SkillStatus{Path: path, Kind: "directory", Status: "inactive"})
		}
		for _, path := range configuration.Profile.SkillRoots {
			status.Skills = append(status.Skills, sessionprofile.SkillStatus{Path: path, Kind: "skill", Status: "inactive"})
		}
	}
	return status, nil
}

// SessionProfile reads immutable creation evidence. Current desired settings are
// returned by SessionConfiguration, independently of native history revisions.
func (s *SessionProfileService) SessionProfile(ctx context.Context, p Principal, sessionID string) (sessionprofile.Binding, error) {
	scope, err := SessionScope(p)
	if err != nil {
		return sessionprofile.Binding{}, err
	}
	return s.config.Store.GetBinding(ctx, scope, sessionID)
}
