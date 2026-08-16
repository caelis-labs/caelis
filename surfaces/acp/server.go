package acp

import (
	"context"
	"io"

	"github.com/caelis-labs/caelis/protocol/acp/server"
)

// ServeStdio exposes agent over the ACP stdio transport.
func ServeStdio(ctx context.Context, agent server.Agent, in io.Reader, out io.Writer) error {
	return server.ServeStdio(ctx, agent, in, out)
}
