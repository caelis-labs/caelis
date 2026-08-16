package gatewayapp

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

func runHeadlessOnceForGatewayAppTest(ctx context.Context, stack *Stack, activeSession session.Session, _ string, input string, opts headless.Options) (headless.Result, error) {
	client, err := appserver.BindSessionClient(
		stack.ControlClient(),
		appserver.Principal{ID: activeSession.UserID},
	)
	if err != nil {
		return headless.Result{}, err
	}
	turns, err := appserver.NewSessionTurnClient(client)
	if err != nil {
		return headless.Result{}, err
	}
	return headless.RunSessionOnce(
		ctx,
		turns,
		appserver.SessionTurnStartRequest{SessionID: activeSession.SessionID, Input: input},
		opts,
	)
}
