package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controldelegation "github.com/caelis-labs/caelis/control/delegation"
	controlsystemagent "github.com/caelis-labs/caelis/control/systemagent"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/discovery"
	internalcontrolclient "github.com/caelis-labs/caelis/internal/controlclient"
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

// ConnectACP persists one model-scoped discovery as a stable Agent. External
// process I/O happens before the fenced configuration transaction.
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
	s.agentRosterMu.Lock()
	defer s.agentRosterMu.Unlock()
	if err := s.rejectReconfigureWhileActive("connect ACP Agent"); err != nil {
		return controlagents.ConnectResult{}, err
	}
	doc, err := s.store.Load()
	if err != nil {
		return controlagents.ConnectResult{}, err
	}
	previous := doc
	next, agent, err := controlagents.UpsertExternalAgent(
		doc.AgentRoster,
		connection,
		model,
		defaults,
		snapshot,
		rosterAgentNameAllowed,
	)
	if err != nil {
		return controlagents.ConnectResult{}, fmt.Errorf("gatewayapp: update Agent roster: %w", err)
	}
	doc.AgentRoster = next
	if err := s.store.Save(doc); err != nil {
		return controlagents.ConnectResult{}, err
	}
	if err := s.refreshConfiguredAgentsFromStore(); err != nil {
		return controlagents.ConnectResult{}, s.rollbackAgentRoster(previous, err)
	}
	if connection.Launcher.Kind == controlagents.LaunchKindManaged &&
		pathWithinRoot(connection.Launcher.Command, filepath.Join(s.managedACPAgentRoot(), "installations")) {
		s.cleanupLegacyManagedACPInstallIfUnused()
	}
	return controlagents.ConnectResult{
		Connection: connection,
		Agents:     []controlagents.Agent{agent},
		Discovery:  snapshot,
	}, nil
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
	return controlagents.ListDisconnectCandidates(doc.AgentRoster), nil
}

// DisconnectACP removes exactly one user-configured external ACP Agent. A
// shared Connection remains until its final Agent reference is removed. Adapter
// installation is deliberately outside the roster transaction and is retained.
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
	s.agentRosterMu.Lock()
	defer s.agentRosterMu.Unlock()
	if err := s.rejectReconfigureWhileActive("disconnect ACP Agent"); err != nil {
		return controlagents.DisconnectResult{}, err
	}
	doc, err := s.store.Load()
	if err != nil {
		return controlagents.DisconnectResult{}, err
	}
	previous := doc
	next, result, err := controlagents.DisconnectExternalAgent(doc.AgentRoster, agentID)
	if err != nil {
		return controlagents.DisconnectResult{}, fmt.Errorf("gatewayapp: %w", err)
	}
	if err := s.rejectRemovedLiveACPAgents(ctx, doc.AgentRoster, next); err != nil {
		return controlagents.DisconnectResult{}, err
	}
	doc.Delegation = resetRemovedDelegationBindings(doc.Delegation, doc.AgentRoster, next)
	doc.AgentRoster = next
	if err := s.store.Save(doc); err != nil {
		return controlagents.DisconnectResult{}, err
	}
	if err := s.refreshConfiguredAgentsFromStore(); err != nil {
		return controlagents.DisconnectResult{}, s.rollbackAgentRoster(previous, err)
	}
	return result, nil
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

func resetRemovedDelegationBindings(
	current controldelegation.Configuration,
	before controlagents.Configuration,
	after controlagents.Configuration,
) controldelegation.Configuration {
	next := current
	for _, agentID := range removedAgentIDs(before, after) {
		next, _ = controldelegation.ResetAgentBindings(next, agentID)
	}
	return next
}

func resetRemovedSystemAgentBindings(
	current controlsystemagent.Configuration,
	before controlagents.Configuration,
	after controlagents.Configuration,
) controlsystemagent.Configuration {
	next := current
	for _, agentID := range removedAgentIDs(before, after) {
		next = controlsystemagent.ResetAgentBindings(next, agentID)
	}
	return next
}

// rejectBoundACPAgent is the Control-owned safety gate for durable controller
// ownership. Closed historical Sessions retain their binding for replay but
// are not recoverable work and therefore do not block roster cleanup.
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
			closed, err := internalcontrolclient.IsSessionClosed(ctx, s.Sessions, active.SessionRef)
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

func (s *Stack) rollbackAgentRoster(previous AppConfig, cause error) error {
	var rollbackErrs []error
	if err := s.store.Save(previous); err != nil {
		rollbackErrs = append(rollbackErrs, fmt.Errorf("gatewayapp: rollback Agent roster save failed: %w", err))
	}
	if err := s.refreshConfiguredAgentsFromStore(); err != nil {
		rollbackErrs = append(rollbackErrs, fmt.Errorf("gatewayapp: rollback Agent assembly refresh failed: %w", err))
	}
	return errors.Join(append([]error{cause}, rollbackErrs...)...)
}
