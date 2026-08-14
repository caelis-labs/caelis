package acpserver

import (
	"context"
	"io"

	"github.com/caelis-labs/caelis/protocol/acp/server"
)

func ServeStdio(ctx context.Context, agent server.Agent, in io.Reader, out io.Writer) error {
	return server.ServeStdio(ctx, agent, in, out)
}
