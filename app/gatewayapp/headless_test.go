package gatewayapp

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

func runHeadlessOnceForGatewayAppTest(ctx context.Context, stack *Stack, activeSession session.Session, _ string, input string, opts headless.Options) (headless.Result, error) {
	client, err := controlclient.BindSessionClient(
		stack.ControlClient(),
		controlclient.Principal{ID: activeSession.UserID},
	)
	if err != nil {
		return headless.Result{}, err
	}
	turns, err := controlclient.NewSessionTurnClient(client)
	if err != nil {
		return headless.Result{}, err
	}
	return headless.RunSessionOnce(
		ctx,
		turns,
		controlclient.SessionTurnStartRequest{SessionID: activeSession.SessionID, Input: input},
		opts,
	)
}
