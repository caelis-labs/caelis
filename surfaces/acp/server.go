package acp

import (
	"context"
	"io"

	"github.com/caelis-labs/caelis/protocol/acp"
)

// ServeStdio exposes agent over the ACP stdio transport.
func ServeStdio(ctx context.Context, agent acp.Agent, in io.Reader, out io.Writer) error {
	return acp.ServeStdio(ctx, agent, in, out)
}
