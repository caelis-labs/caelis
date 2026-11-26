package executil

import (
	"context"
	"errors"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

const terminateTimeout = time.Second

// WaitSession writes initial terminal input, observes output notifications
// until the process is terminal, and returns the session's canonical result.
func WaitSession(ctx context.Context, session sandbox.Session, input []byte) (sandbox.CommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(input) > 0 {
		if err := session.WriteInput(ctx, input); err != nil {
			return abortSession(ctx, session, err)
		}
	}
	status, err := session.Status(ctx)
	if err != nil {
		return abortSession(ctx, session, err)
	}
	cursor := sandbox.OutputCursor{}
	for status.Running {
		observation, observeErr := session.AwaitOutput(ctx, cursor)
		if observeErr != nil {
			return abortSession(ctx, session, observeErr)
		}
		cursor = observation.Cursor
		status = observation.Status
	}
	return session.Result(ctx)
}

func abortSession(ctx context.Context, session sandbox.Session, cause error) (sandbox.CommandResult, error) {
	terminateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminateTimeout)
	defer cancel()
	terminateErr := session.Terminate(terminateCtx)
	result, resultErr := session.Result(context.WithoutCancel(ctx))
	return result, errors.Join(cause, terminateErr, resultErr)
}
