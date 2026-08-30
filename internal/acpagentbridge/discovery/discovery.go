// Package discovery probes external ACP connections for session-scoped model
// and config catalogs without submitting a prompt.
package discovery

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/endpoint"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpcleanup"
)

const defaultPreparationProcessExitGrace = 10 * time.Second

// Service discovers the catalog declared by a temporary empty ACP session.
type Service struct {
	ClientInfo          *acpsdk.Implementation
	Clock               func() time.Time
	SessionCloseTimeout time.Duration
	ProcessExitGrace    time.Duration
	EndpointResolver    endpoint.Resolver
}

func (s Service) startInitializedClient(
	ctx context.Context,
	connection controlagents.Connection,
	workDir string,
) (*client.Client, client.InitializeResponse, error) {
	acpClient, err := client.Start(ctx, client.Config{
		HostedAdapterID:  connection.Launcher.AdapterID,
		ConnectionID:     connection.ID,
		EndpointResolver: s.EndpointResolver,
		Command:          connection.Launcher.Command,
		Args:             append([]string(nil), connection.Launcher.Args...),
		Env:              maps.Clone(connection.Launcher.Env),
		WorkDir:          workDir,
		ClientInfo:       s.ClientInfo,
		TerminalAuth:     controlagents.TerminalAuthenticationAvailable(ctx),
	})
	if err != nil {
		if connection.Launcher.Kind == controlagents.LaunchKindPackageExec {
			return nil, client.InitializeResponse{}, fmt.Errorf(
				"internal/acpagentbridge/discovery: start connection %q through package launcher %q; verify its package cache or choose another launcher: %w",
				connection.ID,
				filepath.Base(connection.Launcher.Command),
				err,
			)
		}
		return nil, client.InitializeResponse{}, fmt.Errorf(
			"internal/acpagentbridge/discovery: start connection %q: %w",
			connection.ID,
			err,
		)
	}
	initialize, err := acpClient.Initialize(ctx)
	if err != nil {
		_ = acpcleanup.CloseClientWithGrace(ctx, acpClient, s.processExitGrace())
		if connection.Launcher.Kind == controlagents.LaunchKindPackageExec {
			return nil, client.InitializeResponse{}, fmt.Errorf(
				"internal/acpagentbridge/discovery: initialize connection %q through package launcher %q; verify its package cache or choose another launcher: %w",
				connection.ID,
				filepath.Base(connection.Launcher.Command),
				err,
			)
		}
		return nil, client.InitializeResponse{}, fmt.Errorf(
			"internal/acpagentbridge/discovery: initialize connection %q: %w",
			connection.ID,
			err,
		)
	}
	return acpClient, initialize, nil
}

func (s Service) sessionCloseTimeout() time.Duration {
	if s.SessionCloseTimeout > 0 {
		return s.SessionCloseTimeout
	}
	return acpcleanup.DefaultSessionCloseTimeout
}

func (s Service) processExitGrace() time.Duration {
	if s.ProcessExitGrace > 0 {
		return s.ProcessExitGrace
	}
	return defaultPreparationProcessExitGrace
}

func hasSessionCapability(resp client.InitializeResponse, name string) bool {
	if resp.AgentCapabilities.SessionCapabilities == nil {
		return false
	}
	_, ok := resp.AgentCapabilities.SessionCapabilities[strings.TrimSpace(name)]
	return ok
}
