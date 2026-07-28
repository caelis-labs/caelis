// Package discovery probes external ACP connections for session-scoped model
// and config catalogs without submitting a prompt.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/authentication"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpcleanup"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/sessionconfig"
	"github.com/caelis-labs/caelis/protocol/acp/client"
)

// Service discovers the catalog declared by a temporary empty ACP session.
type Service struct {
	ClientInfo     *client.Implementation
	Clock          func() time.Time
	CleanupTimeout time.Duration
}

// Discover starts the configured ACP process, initializes it, creates one
// empty session, records its catalog, closes the remote session when supported,
// and always closes the process before returning.
func (s Service) Discover(ctx context.Context, connection controlagents.Connection, cwd string, selectedModelID string) (snapshot controlagents.DiscoverySnapshot, err error) {
	if ctx == nil {
		return controlagents.DiscoverySnapshot{}, fmt.Errorf("internal/acpagentbridge/discovery: context is required")
	}
	connection = controlagents.NormalizeConnection(connection)
	if err := controlagents.ValidateConnection(connection); err != nil {
		return controlagents.DiscoverySnapshot{}, err
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		cwd = strings.TrimSpace(connection.Launcher.WorkDir)
	}
	if cwd == "" {
		return controlagents.DiscoverySnapshot{}, fmt.Errorf("internal/acpagentbridge/discovery: cwd is required")
	}
	workDir := strings.TrimSpace(connection.Launcher.WorkDir)
	if workDir == "" {
		workDir = cwd
	}
	acpClient, initialize, err := s.startInitializedClient(ctx, connection, workDir)
	if err != nil {
		return controlagents.DiscoverySnapshot{}, err
	}
	defer func() {
		err = errors.Join(err, acpcleanup.CloseClientWithin(ctx, acpClient, s.cleanupTimeout()))
	}()

	authConnection := connection
	authConnection.Launcher.WorkDir = workDir
	recovered, recoveryErr := authentication.OpenNewSession(ctx, authentication.RecoveryConfig{
		Mode:           authentication.RecoveryInteractive,
		Client:         acpClient,
		Initialize:     initialize,
		Methods:        authentication.Methods(initialize),
		AgentID:        connection.ID,
		Connection:     authConnection,
		Authentication: connection.Authentication,
		Restart: func(restartCtx context.Context) (*client.Client, client.InitializeResponse, error) {
			return s.startInitializedClient(restartCtx, connection, workDir)
		},
		CleanupTimeout: s.cleanupTimeout(),
	}, cwd)
	acpClient = recovered.Client
	if recoveryErr != nil {
		return controlagents.DiscoverySnapshot{}, fmt.Errorf("internal/acpagentbridge/discovery: create discovery session for %q: %w", connection.ID, recoveryErr)
	}
	initialize = recovered.Initialize
	created := recovered.Value
	sessionID := strings.TrimSpace(created.SessionID)
	if sessionID == "" {
		return controlagents.DiscoverySnapshot{}, fmt.Errorf("internal/acpagentbridge/discovery: connection %q returned an empty discovery session id", connection.ID)
	}

	state := sessionconfig.State{
		ConfigOptions: created.ConfigOptions,
		Models:        created.Models,
	}
	selectedModelID = strings.TrimSpace(selectedModelID)
	wireModelID := selectedModelID
	if controlagents.IsDefaultRemoteModelID(wireModelID) {
		wireModelID = ""
	}
	if wireModelID != "" {
		state, err = sessionconfig.Apply(ctx, acpClient, sessionID, state, controlagents.SessionOptions{ModelID: wireModelID})
		if err != nil {
			return controlagents.DiscoverySnapshot{}, fmt.Errorf("internal/acpagentbridge/discovery: select model %q for discovery connection %q: %w", selectedModelID, connection.ID, err)
		}
	}
	snapshot = sessionconfig.Snapshot(connection, cwd, initialize.ProtocolVersion, state)
	snapshot.SelectedModelID = selectedModelID
	snapshot.Authentication = controlagents.NormalizeAuthentication(recovered.Authentication)
	clock := s.Clock
	if clock == nil {
		clock = time.Now
	}
	snapshot.DiscoveredAt = clock().UTC()
	if hasSessionCapability(initialize, "close") {
		if closeErr := acpcleanup.CloseSessionWithin(ctx, acpClient, sessionID, s.cleanupTimeout()); closeErr != nil {
			return controlagents.DiscoverySnapshot{}, fmt.Errorf("internal/acpagentbridge/discovery: close discovery session %q: %w", sessionID, closeErr)
		}
	}
	return snapshot, nil
}

func (s Service) startInitializedClient(
	ctx context.Context,
	connection controlagents.Connection,
	workDir string,
) (*client.Client, client.InitializeResponse, error) {
	acpClient, err := client.Start(ctx, client.Config{
		Command:      connection.Launcher.Command,
		Args:         append([]string(nil), connection.Launcher.Args...),
		Env:          maps.Clone(connection.Launcher.Env),
		WorkDir:      workDir,
		ClientInfo:   s.ClientInfo,
		TerminalAuth: controlagents.TerminalAuthenticationAvailable(ctx),
	})
	if err != nil {
		return nil, client.InitializeResponse{}, fmt.Errorf(
			"internal/acpagentbridge/discovery: start connection %q: %w",
			connection.ID,
			err,
		)
	}
	initialize, err := acpClient.Initialize(ctx)
	if err != nil {
		_ = acpcleanup.CloseClientWithin(ctx, acpClient, s.cleanupTimeout())
		return nil, client.InitializeResponse{}, fmt.Errorf(
			"internal/acpagentbridge/discovery: initialize connection %q: %w",
			connection.ID,
			err,
		)
	}
	return acpClient, initialize, nil
}

func (s Service) cleanupTimeout() time.Duration {
	if s.CleanupTimeout > 0 {
		return s.CleanupTimeout
	}
	return acpcleanup.DefaultTimeout
}

func hasSessionCapability(resp client.InitializeResponse, name string) bool {
	if resp.AgentCapabilities.SessionCapabilities == nil {
		return false
	}
	_, ok := resp.AgentCapabilities.SessionCapabilities[strings.TrimSpace(name)]
	return ok
}
