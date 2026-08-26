package gatewayapp

import (
	"context"
	"io"

	"github.com/caelis-labs/caelis/adapters/codex"
	adapterhostimpl "github.com/caelis-labs/caelis/app/gatewayapp/internal/adapterhost"
	controladapterhost "github.com/caelis-labs/caelis/control/adapterhost"
)

type codexHostedBackend struct {
	*codex.Backend
}

func (b codexHostedBackend) ServeACP(
	ctx context.Context,
	channel controladapterhost.ChannelContext,
	input io.Reader,
	output io.Writer,
) error {
	return b.Backend.ServeACP(ctx, codex.ConnectionOptions{
		ConnectionID: channel.ConnectionID,
		Workspace: codex.WorkspacePolicy{
			AllowedRoots:  append([]string(nil), channel.AllowedRoots...),
			WritableRoots: append([]string(nil), channel.WritableRoots...),
		},
	}, input, output)
}

func newHostedAdapterManager() (*adapterhostimpl.Manager, error) {
	return adapterhostimpl.NewManager(adapterhostimpl.Registration{
		ID: controladapterhost.CodexAdapterID, Name: "Codex",
		Command: "codex", Args: []string{"app-server", "--stdio"},
		NewBackend: func(ctx context.Context, input io.Reader, output io.Writer) (adapterhostimpl.Backend, error) {
			backend, err := codex.NewBackend(ctx, input, output)
			if err != nil {
				return nil, err
			}
			return codexHostedBackend{Backend: backend}, nil
		},
	})
}

// AdapterHost returns the focused Host-managed adapter capability used by the
// private adapter transport. It does not expose the process supervisor.
func (s *Stack) AdapterHost() controladapterhost.Service {
	if s == nil || s.adapterHost == nil {
		return nil
	}
	return s.adapterHost
}
