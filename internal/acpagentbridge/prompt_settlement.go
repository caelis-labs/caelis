package acpagentbridge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

const promptCancellationSettlementTimeout = 30 * time.Second

// settlePromptTurn retains the admitted execution's observation until its
// terminal. Once normal ACP forwarding fails, approvals cannot be serviced
// reliably: request cancellation and bound settlement rather than silently
// drain a producer that may be waiting for permission. Close alone only detaches
// observation and is never execution-termination evidence.
func settlePromptTurn(ctx context.Context, turn controlprompt.Turn, terminal *eventstream.Envelope, cause error) error {
	defer turn.Close()
	if terminal != nil {
		return promptTerminalCause(*terminal, cause)
	}
	waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), promptCancellationSettlementTimeout)
	defer cancel()
	turn.Cancel()
	events := turn.Events()
	for {
		if events == nil {
			return unsettledPromptError(turn, cause)
		}
		select {
		case <-waitCtx.Done():
			return unsettledPromptError(turn, cause)
		case env, open := <-events:
			if !open {
				return unsettledPromptError(turn, cause)
			}
			if eventstream.IsTurnTerminalLifecycle(env) {
				return promptTerminalCause(env, cause)
			}
		}
	}
}

func promptTerminalCause(env eventstream.Envelope, cause error) error {
	switch env.Lifecycle.State {
	case eventstream.LifecycleStateCompleted:
		// Execution completion wins a race with cancellation of its observer.
		// Keep unrelated projection failures, but never encode a completed Turn
		// as a successful ACP cancelled response.
		if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
			return nil
		}
		return cause
	case eventstream.LifecycleStateCancelled, "canceled":
		if cause != nil && !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
			return cause
		}
		return context.Canceled
	default:
		return fmt.Errorf("ACP prompt execution ended with state %s: %v", env.Lifecycle.State, cause) //nolint:errorlint // A cancelled observation must not become an ACP cancelled Turn.
	}
}

func unsettledPromptError(turn controlprompt.Turn, cause error) error {
	if reporter, ok := turn.(interface{ Err() error }); ok {
		cause = errors.Join(cause, reporter.Err())
	}
	// Do not unwrap a cancelled request context here: neither the product
	// prompt adapter nor the SDK may turn an unproven execution outcome into
	// a successful cancelled stop reason. The SDK encodes this ordinary handler
	// failure as a standard JSON-RPC internal error, not a private ACP status.
	return fmt.Errorf("ACP prompt observation ended without execution terminal: %v", cause) //nolint:errorlint // Intentionally stop cancellation unwrapping at the execution-outcome boundary.
}
