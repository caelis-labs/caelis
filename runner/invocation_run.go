package runner

import (
	"context"
	"fmt"

	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/session"
)

type agentRunStep struct {
	completed bool
	retry     bool
	err       error
}

func (r *Runner) runAgentWithPersistence(
	ctx context.Context,
	ref session.Ref,
	invID string,
	runAgent agent.Agent,
	invCtx *invocationContext,
	observer *toolObserverBridge,
	yield func(session.Event, error) bool,
) (bool, error) {
	retriedOverflow := false
	for {
		step := r.runAgentOnce(ctx, ref, invID, runAgent, invCtx, observer, yield, retriedOverflow)
		if step.err != nil || !step.completed {
			return step.completed, step.err
		}
		if step.retry {
			retriedOverflow = true
			continue
		}
		return true, nil
	}
}

func (r *Runner) runAgentOnce(
	ctx context.Context,
	ref session.Ref,
	invID string,
	runAgent agent.Agent,
	invCtx *invocationContext,
	observer *toolObserverBridge,
	yield func(session.Event, error) bool,
	retriedOverflow bool,
) agentRunStep {
	for evt, err := range runAgent.Run(invCtx) {
		if err != nil {
			if !retriedOverflow && isContextOverflowError(err) {
				compacted, ok, err := r.compactForOverflowRetry(ctx, ref, invCtx.priorMessages)
				if err != nil {
					return agentRunStep{err: err}
				}
				if ok {
					invCtx.priorMessages = compacted
					return agentRunStep{completed: true, retry: true}
				}
			}
			return agentRunStep{err: fmt.Errorf("runner: agent error: %w", err)}
		}

		if !drainObserverBridge(observer, yield) {
			return agentRunStep{}
		}
		completed, err := r.persistOrYieldInvocationEvent(ctx, ref, invID, evt, yield)
		if err != nil || !completed {
			return agentRunStep{completed: completed, err: err}
		}
	}
	return agentRunStep{completed: true}
}

func (r *Runner) persistOrYieldInvocationEvent(
	ctx context.Context,
	ref session.Ref,
	invID string,
	evt session.Event,
	yield func(session.Event, error) bool,
) (bool, error) {
	evt.SessionRef = ref
	evt.RunID = invID
	if evt.Visibility.IsTransient() {
		return yield(evt, nil), nil
	}

	persisted, err := r.cfg.Sessions.AppendEvent(ctx, ref, evt)
	if err != nil {
		return false, fmt.Errorf("runner: persist event: %w", err)
	}
	return yield(persisted, nil), nil
}
