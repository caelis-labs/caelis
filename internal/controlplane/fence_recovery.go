package controlplane

import (
	"context"
	"fmt"
	"strings"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type fencedExecutionKey struct {
	sessionID string
	fenceID   string
}

type activeFencedExecution struct {
	key   fencedExecutionKey
	guard *sessionFenceGuard

	mu        sync.Mutex
	runner    *fencedRunner
	changed   chan struct{}
	done      chan struct{}
	completed bool
}

func newActiveFencedExecution(key fencedExecutionKey, guard *sessionFenceGuard) *activeFencedExecution {
	return &activeFencedExecution{
		key:     key,
		guard:   guard,
		changed: make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func (e *activeFencedExecution) setRunner(runner *fencedRunner) {
	if e == nil || runner == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.completed {
		return
	}
	e.runner = runner
	close(e.changed)
	e.changed = make(chan struct{})
}

func (e *activeFencedExecution) complete() {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.completed {
		return
	}
	e.completed = true
	close(e.done)
	close(e.changed)
}

func (e *activeFencedExecution) waitForQuiescence(ctx context.Context) error {
	for {
		e.mu.Lock()
		completed := e.completed
		runner := e.runner
		changed := e.changed
		done := e.done
		e.mu.Unlock()
		if completed {
			return e.guard.finish()
		}
		if runner != nil {
			completed, err := runner.waitForProducer(ctx)
			if !completed {
				return err
			}
			// The fence-loss error is expected on this path. Preserve only
			// durable release/reconciliation failures for the new admission.
			_ = runner.finish()
			return runner.guard.finish()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return e.guard.finish()
		case <-changed:
		}
	}
}

func (r *FencedRuntime) registerFencedExecution(
	ref session.SessionRef,
	fenceID string,
	guard *sessionFenceGuard,
) *activeFencedExecution {
	key := fencedExecutionKey{
		sessionID: strings.TrimSpace(session.NormalizeSessionRef(ref).SessionID),
		fenceID:   strings.TrimSpace(fenceID),
	}
	execution := newActiveFencedExecution(key, guard)
	r.executionsMu.Lock()
	r.executions[key] = execution
	r.executionsMu.Unlock()
	return execution
}

func (r *FencedRuntime) completeFencedExecution(execution *activeFencedExecution) {
	if execution == nil {
		return
	}
	execution.complete()
	if !execution.guard.released() {
		return
	}
	r.releaseFencedExecution(execution)
}

func (r *FencedRuntime) releaseFencedExecution(execution *activeFencedExecution) {
	if execution == nil {
		return
	}
	execution.mu.Lock()
	completed := execution.completed
	execution.mu.Unlock()
	if !completed {
		return
	}
	r.executionsMu.Lock()
	if current := r.executions[execution.key]; current == execution {
		delete(r.executions, execution.key)
	}
	r.executionsMu.Unlock()
}

func (r *FencedRuntime) fencedExecutions(ref session.SessionRef) []*activeFencedExecution {
	sessionID := strings.TrimSpace(session.NormalizeSessionRef(ref).SessionID)
	r.executionsMu.Lock()
	defer r.executionsMu.Unlock()
	executions := make([]*activeFencedExecution, 0, len(r.executions))
	for key, execution := range r.executions {
		if key.sessionID == sessionID && execution != nil {
			executions = append(executions, execution)
		}
	}
	return executions
}

// RecoverLostRun quiesces process-local producers only after their durable
// Session execution fence is proven absent or replaced. It does not acquire a
// new fence or repair durable execution state; the next ordinary Runtime.Run
// performs those steps under a fresh fencing token.
func (r *FencedRuntime) RecoverLostRun(ctx context.Context, ref session.SessionRef) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	executions := r.fencedExecutions(ref)
	if len(executions) == 0 {
		return false, nil
	}
	losses := make([]error, len(executions))
	for i, execution := range executions {
		loss, err := execution.guard.recoveryLoss(ctx)
		if err != nil {
			return false, err
		}
		if loss == nil {
			return false, nil
		}
		losses[i] = loss
	}
	for i, execution := range executions {
		execution.guard.recordLoss(losses[i])
	}
	for _, execution := range executions {
		if err := execution.waitForQuiescence(ctx); err != nil {
			return false, fmt.Errorf("controlplane: wait for lost session producer: %w", err)
		}
	}
	return true, nil
}

func (g *sessionFenceGuard) recoveryLoss(ctx context.Context) (error, error) {
	g.mu.Lock()
	fence := g.fence
	loss := g.lossErr
	g.mu.Unlock()
	if loss != nil {
		return loss, nil
	}
	reader, ok := g.fences.(session.SessionFenceReader)
	if !ok {
		return nil, nil
	}
	durable, err := reader.SessionFence(ctx, fence.SessionRef)
	if err != nil {
		return nil, fmt.Errorf("controlplane: inspect session fence for lost-run recovery: %w", err)
	}
	if strings.TrimSpace(durable.FenceID) == "" {
		return errSessionFenceOwnershipLost, nil
	}
	if durable.FenceID != fence.FenceID || durable.OwnerID != fence.OwnerID || durable.FencingToken != fence.FencingToken {
		return errSessionFenceOwnershipLost, nil
	}
	return nil, nil
}

func baseFencedRunner(runner agent.Runner) *fencedRunner {
	r, _ := runner.(*fencedRunner)
	return r
}
