package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/control/modelprofile"
	modelprofilebuilder "github.com/caelis-labs/caelis/control/modelprofile/builder"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/discovery"
	"github.com/caelis-labs/caelis/internal/version"
	"github.com/caelis-labs/caelis/protocol/acp/client"
)

const disconnectSessionPageLimit = 200

// DiscoverACPConnection probes an external launcher without taking the global
// runtime-reconfiguration lock. Installation has its own narrower lock and the
// temporary ACP process is always closed by the discovery service.
func (s *Stack) DiscoverACPConnection(ctx context.Context, req controlagents.ConnectRequest) (controlagents.DiscoverySnapshot, error) {
	if s == nil {
		return controlagents.DiscoverySnapshot{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req = controlagents.NormalizeConnectRequest(req)
	connection, err := s.resolveACPConnectionLauncher(ctx, req)
	if err != nil {
		return controlagents.DiscoverySnapshot{}, err
	}
	controlagents.ReportSetupProgress(ctx, controlagents.SetupProgress{
		AdapterID: req.AdapterID,
		Phase:     controlagents.SetupPhaseDiscovering,
		Detail:    "Starting the ACP Agent and discovering its models",
	})
	cwd := firstNonEmpty(req.CWD, s.Workspace.CWD)
	return s.acpDiscoveryService().Discover(ctx, connection, cwd, req.ModelID)
}

// ConnectACP persists one stable external Agent and one model-scoped
// ModelProfile. External process I/O happens before the fenced configuration
// transaction.
func (s *Stack) ConnectACP(ctx context.Context, req controlagents.ConnectRequest) (controlagents.ConnectResult, error) {
	if s == nil || s.store == nil {
		return controlagents.ConnectResult{}, fmt.Errorf("gatewayapp: app config store unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req = controlagents.NormalizeConnectRequest(req)
	if req.ModelID == "" {
		return controlagents.ConnectResult{}, fmt.Errorf("gatewayapp: select one ACP model")
	}

	connection, err := s.resolveACPConnectionLauncher(ctx, req)
	if err != nil {
		return controlagents.ConnectResult{}, err
	}
	cwd := firstNonEmpty(req.CWD, s.Workspace.CWD)
	var snapshot controlagents.DiscoverySnapshot
	if discoveryMatches(req.Discovery, connection, cwd, req.ModelID) {
		snapshot = controlagents.NormalizeDiscoverySnapshot(*req.Discovery)
	} else {
		controlagents.ReportSetupProgress(ctx, controlagents.SetupProgress{
			AdapterID: req.AdapterID,
			Phase:     controlagents.SetupPhaseDiscovering,
			Detail:    "Starting the ACP Agent and validating the selected model",
		})
		snapshot, err = s.acpDiscoveryService().Discover(ctx, connection, cwd, req.ModelID)
		if err != nil {
			return controlagents.ConnectResult{}, err
		}
	}
	model, defaults, err := controlagents.ResolveDiscoverySelection(snapshot, req.ModelID, req.ConfigValues)
	if err != nil {
		return controlagents.ConnectResult{}, fmt.Errorf("gatewayapp: %w", err)
	}

	s.reconfigureMu.Lock()
	defer s.reconfigureMu.Unlock()
	s.assemblyMutationMu.Lock()
	defer s.assemblyMutationMu.Unlock()
	if err := s.rejectReconfigureWhileActive("connect ACP Agent"); err != nil {
		return controlagents.ConnectResult{}, err
	}
	doc, err := s.store.Load()
	if err != nil {
		return controlagents.ConnectResult{}, err
	}
	previous := doc
	next, agent, err := controlagents.UpsertExternalConnection(
		doc.ExternalAgents,
		connection,
		snapshot,
		externalAgentNameAllowed,
	)
	if err != nil {
		return controlagents.ConnectResult{}, fmt.Errorf("gatewayapp: update external Agent configuration: %w", err)
	}
	profile, err := modelprofilebuilder.FromACP(agent, connection, model, defaults, snapshot)
	if err != nil {
		return controlagents.ConnectResult{}, fmt.Errorf("gatewayapp: build ACP model profile: %w", err)
	}
	doc.ExternalAgents = next
	doc.ModelProfiles, err = modelprofile.Upsert(doc.ModelProfiles, profile)
	if err != nil {
		return controlagents.ConnectResult{}, fmt.Errorf("gatewayapp: update model profile catalog: %w", err)
	}
	result := controlagents.ConnectResult{
		Connection: connection,
		Profiles:   []modelprofile.ModelProfile{profile},
		Discovery:  snapshot,
	}
	saveErr := s.store.Save(doc)
	if saveErr != nil && !configstore.WriteCommitted(saveErr) {
		return controlagents.ConnectResult{}, saveErr
	}
	if err := s.refreshConfiguredAgentsFromStore(); err != nil {
		if saveErr != nil {
			return result, errors.Join(saveErr, err)
		}
		return controlagents.ConnectResult{}, s.rollbackExternalAgentConfig(previous, err)
	}
	if connection.Launcher.Kind == controlagents.LaunchKindManaged &&
		pathWithinRoot(connection.Launcher.Command, filepath.Join(s.managedACPAgentRoot(), "installations")) {
		s.cleanupLegacyManagedACPInstallIfUnused()
	}
	return result, saveErr
}

// DisconnectCandidates returns only user-configured external ACP Agents. It
// excludes model-backed, built-in, system, and plugin-provided Agents because
// those lifecycles are owned by their respective Control capabilities.
func (s *Stack) DisconnectCandidates(ctx context.Context) ([]controlagents.DisconnectCandidate, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("gatewayapp: app config store unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.reconfigureMu.Lock()
	defer s.reconfigureMu.Unlock()
	doc, err := s.store.Load()
	if err != nil {
		return nil, err
	}
	return controlagents.ListDisconnectCandidates(doc.ExternalAgents), nil
}

// DisconnectACP removes one connection-scoped external ACP Agent and every
// sibling ModelProfile backed by it. Adapter installation is deliberately
// outside the configuration transaction and is retained.
func (s *Stack) DisconnectACP(ctx context.Context, agentID string) (controlagents.DisconnectResult, error) {
	if s == nil || s.store == nil {
		return controlagents.DisconnectResult{}, fmt.Errorf("gatewayapp: app config store unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return controlagents.DisconnectResult{}, err
	}

	s.reconfigureMu.Lock()
	defer s.reconfigureMu.Unlock()
	s.assemblyMutationMu.Lock()
	defer s.assemblyMutationMu.Unlock()
	if err := s.rejectReconfigureWhileActive("disconnect ACP Agent"); err != nil {
		return controlagents.DisconnectResult{}, err
	}
	doc, err := s.store.Load()
	if err != nil {
		return controlagents.DisconnectResult{}, err
	}
	previous := doc
	next, result, err := controlagents.DisconnectExternalAgent(doc.ExternalAgents, agentID)
	if err != nil {
		return controlagents.DisconnectResult{}, fmt.Errorf("gatewayapp: %w", err)
	}
	if err := s.rejectRemovedLiveACPAgents(ctx, doc.ExternalAgents, next); err != nil {
		return controlagents.DisconnectResult{}, err
	}
	for _, profile := range modelprofile.NormalizeConfiguration(doc.ModelProfiles).Profiles {
		if profile.Kind() != modelprofile.BackendACP || profile.Backend.ACP.AgentID != result.Agent.ID {
			continue
		}
		doc.AgentBindings, err = agentbinding.PrepareProfileRemoval(doc.AgentBindings, profile.ID)
		if err != nil {
			return controlagents.DisconnectResult{}, err
		}
		doc.ModelProfiles = modelprofile.Remove(doc.ModelProfiles, profile.ID)
	}
	doc.ExternalAgents = next
	saveErr := s.store.Save(doc)
	if saveErr != nil && !configstore.WriteCommitted(saveErr) {
		return controlagents.DisconnectResult{}, saveErr
	}
	if err := s.refreshConfiguredAgentsFromStore(); err != nil {
		if saveErr != nil {
			return result, errors.Join(saveErr, err)
		}
		return controlagents.DisconnectResult{}, s.rollbackExternalAgentConfig(previous, err)
	}
	return result, saveErr
}

func (s *Stack) rejectRemovedLiveACPAgents(ctx context.Context, before controlagents.Configuration, after controlagents.Configuration) error {
	for _, agentID := range removedAgentIDs(before, after) {
		if err := s.rejectBoundACPAgent(ctx, agentID); err != nil {
			return err
		}
	}
	return nil
}

func removedAgentIDs(before controlagents.Configuration, after controlagents.Configuration) []string {
	afterIDs := make(map[string]struct{}, len(after.Agents))
	for _, agent := range controlagents.NormalizeConfiguration(after).Agents {
		afterIDs[strings.ToLower(strings.TrimSpace(agent.ID))] = struct{}{}
	}
	removed := make([]string, 0)
	for _, agent := range controlagents.NormalizeConfiguration(before).Agents {
		if _, retained := afterIDs[strings.ToLower(strings.TrimSpace(agent.ID))]; retained {
			continue
		}
		removed = append(removed, agent.ID)
	}
	return removed
}

// rejectBoundACPAgent is the Control-owned safety gate for durable controller
// ownership. Closed historical Sessions retain their binding for replay but
// are not recoverable work and therefore do not block external configuration cleanup.
func (s *Stack) rejectBoundACPAgent(ctx context.Context, agentID string) error {
	if s.Sessions == nil {
		return fmt.Errorf("gatewayapp: session service unavailable while disconnecting Agent %q", agentID)
	}
	agentID = strings.TrimSpace(agentID)
	for cursor := ""; ; {
		listed, err := s.Sessions.ListSessions(ctx, session.ListSessionsRequest{
			Cursor: cursor,
			Limit:  disconnectSessionPageLimit,
		})
		if err != nil {
			return fmt.Errorf("gatewayapp: list Sessions before disconnecting Agent %q: %w", agentID, err)
		}
		for _, summary := range listed.Sessions {
			active, err := s.Sessions.Session(ctx, summary.SessionRef)
			if err != nil {
				return fmt.Errorf("gatewayapp: load Session %q before disconnecting Agent %q: %w", summary.SessionID, agentID, err)
			}
			controllerAgent := firstNonEmpty(active.Controller.AgentName, active.Controller.ControllerID, active.Controller.Label)
			if active.Controller.Kind != session.ControllerKindACP ||
				!strings.EqualFold(strings.TrimSpace(controllerAgent), agentID) {
				continue
			}
			closed, err := controlclient.IsSessionClosed(ctx, s.Sessions, active.SessionRef)
			if err != nil {
				return fmt.Errorf("gatewayapp: inspect Session %q before disconnecting Agent %q: %w", active.SessionID, agentID, err)
			}
			if !closed {
				return &controlagents.AgentInUseError{AgentID: agentID, SessionID: active.SessionID}
			}
		}
		next := strings.TrimSpace(listed.NextCursor)
		if next == "" {
			return nil
		}
		if next == cursor {
			return fmt.Errorf("gatewayapp: list Sessions before disconnecting Agent %q returned a repeated cursor", agentID)
		}
		cursor = next
	}
}

func (s *Stack) acpDiscoveryService() discovery.Service {
	return discovery.Service{ClientInfo: &client.Implementation{
		Name:    firstNonEmpty(s.AppName, "caelis"),
		Version: version.String(),
	}}
}

func discoveryMatches(snapshot *controlagents.DiscoverySnapshot, connection controlagents.Connection, cwd string, modelID string) bool {
	if snapshot == nil {
		return false
	}
	normalized := controlagents.NormalizeDiscoverySnapshot(*snapshot)
	return normalized.ConnectionID == connection.ID &&
		normalized.LaunchFingerprint == controlagents.LaunchFingerprint(connection.Launcher) &&
		strings.TrimSpace(normalized.CWD) == strings.TrimSpace(cwd) &&
		normalized.SelectedModelID == strings.TrimSpace(modelID)
}

func (s *Stack) rollbackExternalAgentConfig(previous AppConfig, cause error) error {
	var rollbackErrs []error
	if err := s.store.Save(previous); err != nil {
		rollbackErrs = append(rollbackErrs, fmt.Errorf("gatewayapp: rollback external Agent config save failed: %w", err))
	}
	if err := s.refreshConfiguredAgentsFromStore(); err != nil {
		rollbackErrs = append(rollbackErrs, fmt.Errorf("gatewayapp: rollback Agent assembly refresh failed: %w", err))
	}
	return errors.Join(append([]error{cause}, rollbackErrs...)...)
}
