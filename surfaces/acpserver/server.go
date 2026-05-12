package acpserver

import (
	"context"
	"io"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	"github.com/OnslaughtSnail/caelis/protocol/acp/server"
)

func New(stack *gatewayapp.Stack) (server.Agent, error) {
	return stack.ACPAgent()
}

func ServeStdio(ctx context.Context, stack *gatewayapp.Stack, in io.Reader, out io.Writer) error {
	agent, err := New(stack)
	if err != nil {
		return err
	}
	return server.ServeStdio(ctx, agent, in, out)
}
