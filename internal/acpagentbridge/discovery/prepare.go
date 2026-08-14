package discovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/authentication"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpcleanup"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/sessionconfig"
	"github.com/caelis-labs/caelis/protocol/acp/client"
)

// PrepareState classifies the result of one non-prompting ACP prepare probe.
type PrepareState string

const (
	// PrepareReady means the temporary session completed and Snapshot is ready.
	PrepareReady PrepareState = "ready"
	// PrepareNeedsAuth means session/new returned auth_required. The caller must
	// explicitly choose one of AuthenticationMethods and invoke Prepare again.
	PrepareNeedsAuth PrepareState = "needs_auth"
	// PrepareUnknownCleanup means preparation produced useful evidence, but the
	// temporary Session or process could not be proven closed within the bound.
	PrepareUnknownCleanup PrepareState = "unknown_cleanup"
)

// PrepareRequest supplies one connection probe and an optional exact
// authentication method. AuthenticationMethodID is never inferred from a
// context-installed selector.
type PrepareRequest struct {
	Connection             controlagents.Connection
	CWD                    string
	SelectedModelID        string
	AuthenticationMethodID string
}

// PrepareResult returns either a normal discovery snapshot, a declared
// authentication challenge, or evidence whose cleanup could not be proven.
type PrepareResult struct {
	State                 PrepareState
	Snapshot              controlagents.DiscoverySnapshot
	Authentication        controlagents.Authentication
	AuthenticationMethods []controlagents.AuthenticationMethod
}

// Prepare starts one temporary ACP process and Session without prompting for
// authentication method selection. An auth_required response is returned as a
// typed challenge unless AuthenticationMethodID explicitly selects one of the
// methods advertised by initialize.
func (s Service) Prepare(ctx context.Context, request PrepareRequest) (result PrepareResult, err error) {
	connection, cwd, workDir, err := normalizePrepareRequest(ctx, request)
	if err != nil {
		return PrepareResult{}, err
	}
	acpClient, initialize, err := s.startInitializedClient(ctx, connection, workDir)
	if err != nil {
		return PrepareResult{}, err
	}
	defer func() {
		if acpClient == nil {
			return
		}
		if cleanupErr := acpcleanup.CloseClientWithin(ctx, acpClient, s.cleanupTimeout()); cleanupErr != nil {
			result.State = PrepareUnknownCleanup
			err = errors.Join(err, fmt.Errorf(
				"internal/acpagentbridge/discovery: close prepare client for %q: %w",
				connection.ID,
				cleanupErr,
			))
		}
	}()

	methods := authentication.Methods(initialize)
	result.AuthenticationMethods = controlagents.CloneAuthenticationMethods(methods)
	authConnection := connection
	authConnection.Launcher.WorkDir = workDir
	recovered, recoveryErr := authentication.OpenNewSession(ctx, authentication.RecoveryConfig{
		Mode:       authentication.RecoveryProbe,
		Client:     acpClient,
		Initialize: initialize,
		Methods:    methods,
		AgentID:    connection.ID,
		Connection: authConnection,
		Authentication: controlagents.Authentication{
			MethodID: strings.TrimSpace(request.AuthenticationMethodID),
		},
		Restart: func(restartCtx context.Context) (*client.Client, client.InitializeResponse, error) {
			return s.startInitializedClient(restartCtx, connection, workDir)
		},
		CleanupTimeout: s.cleanupTimeout(),
	}, cwd, nil)
	acpClient = recovered.Client
	result.Authentication = controlagents.NormalizeAuthentication(recovered.Authentication)
	if len(recovered.AuthenticationMethods) > 0 {
		result.AuthenticationMethods = controlagents.CloneAuthenticationMethods(recovered.AuthenticationMethods)
	}
	if recovered.CleanupUnknown {
		result.State = PrepareUnknownCleanup
	}
	if recovered.NeedsAuthentication {
		result.State = PrepareNeedsAuth
		return result, nil
	}
	if recoveryErr != nil {
		return result, fmt.Errorf(
			"internal/acpagentbridge/discovery: create prepare session for %q: %w",
			connection.ID,
			recoveryErr,
		)
	}

	initialize = recovered.Initialize
	created := recovered.Value
	sessionID := strings.TrimSpace(created.SessionID)
	if sessionID == "" {
		return result, fmt.Errorf(
			"internal/acpagentbridge/discovery: connection %q returned an empty prepare session id",
			connection.ID,
		)
	}
	state := sessionconfig.State{ConfigOptions: created.ConfigOptions, Models: created.Models}
	selectedModelID := strings.TrimSpace(request.SelectedModelID)
	wireModelID := selectedModelID
	if controlagents.IsDefaultRemoteModelID(wireModelID) {
		wireModelID = ""
	}
	if wireModelID != "" {
		state, err = sessionconfig.Apply(ctx, acpClient, sessionID, state, controlagents.SessionOptions{ModelID: wireModelID})
		if err != nil {
			return result, fmt.Errorf(
				"internal/acpagentbridge/discovery: select model %q for prepare connection %q: %w",
				selectedModelID,
				connection.ID,
				err,
			)
		}
	}
	result.Snapshot = sessionconfig.Snapshot(connection, cwd, initialize.ProtocolVersion, state)
	result.Snapshot.SelectedModelID = selectedModelID
	result.Snapshot.Authentication = result.Authentication
	clock := s.Clock
	if clock == nil {
		clock = time.Now
	}
	result.Snapshot.DiscoveredAt = clock().UTC()
	if hasSessionCapability(initialize, "close") {
		if closeErr := acpcleanup.CloseSessionWithin(ctx, acpClient, sessionID, s.cleanupTimeout()); closeErr != nil {
			result.State = PrepareUnknownCleanup
			return result, fmt.Errorf(
				"internal/acpagentbridge/discovery: close prepare session %q: %w",
				sessionID,
				closeErr,
			)
		}
	}
	result.State = PrepareReady
	return result, nil
}

func normalizePrepareRequest(
	ctx context.Context,
	request PrepareRequest,
) (controlagents.Connection, string, string, error) {
	if ctx == nil {
		return controlagents.Connection{}, "", "", fmt.Errorf("internal/acpagentbridge/discovery: context is required")
	}
	connection := controlagents.NormalizeConnection(request.Connection)
	if err := controlagents.ValidateConnection(connection); err != nil {
		return controlagents.Connection{}, "", "", err
	}
	cwd := strings.TrimSpace(request.CWD)
	if cwd == "" {
		cwd = strings.TrimSpace(connection.Launcher.WorkDir)
	}
	if cwd == "" {
		return controlagents.Connection{}, "", "", fmt.Errorf("internal/acpagentbridge/discovery: cwd is required")
	}
	workDir := strings.TrimSpace(connection.Launcher.WorkDir)
	if workDir == "" {
		workDir = cwd
	}
	return connection, cwd, workDir, nil
}
