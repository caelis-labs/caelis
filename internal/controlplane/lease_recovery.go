package controlplane

import (
	"context"
	"fmt"
	"strings"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type leasedExecutionKey struct {
	sessionID string
	leaseID   string
}

type activeLeasedExecution struct {
	key   leasedExecutionKey
	guard *sessionLeaseGuard

	mu        sync.Mutex
	runner    *leasedRunner
	changed   chan struct{}
	done      chan struct{}
	completed bool
}

func newActiveLeasedExecution(key leasedExecutionKey, guard *sessionLeaseGuard) *activeLeasedExecution {
	return &activeLeasedExecution{
		key:     key,
		guard:   guard,
		changed: make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func (e *activeLeasedExecution) setRunner(runner *leasedRunner) {
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

func (e *activeLeasedExecution) complete() {
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

func (e *activeLeasedExecution) waitForQuiescence(ctx context.Context) error {
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
			if waiter, ok := runner.inner.(agent.RunnerCompletionWaiter); ok {
				if err := waiter.WaitCompletion(ctx); err != nil {
					return err
				}
				// The lease-loss error is expected on this path. Preserve only
				// durable release/reconciliation failures for the new admission.
				_ = runner.finish()
				return runner.guard.finish()
			}
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

func (r *LeasedRuntime) registerLeasedExecution(
	ref session.SessionRef,
	leaseID string,
	guard *sessionLeaseGuard,
) *activeLeasedExecution {
	key := leasedExecutionKey{
		sessionID: strings.TrimSpace(session.NormalizeSessionRef(ref).SessionID),
		leaseID:   strings.TrimSpace(leaseID),
	}
	execution := newActiveLeasedExecution(key, guard)
	r.executionsMu.Lock()
	r.executions[key] = execution
	r.executionsMu.Unlock()
	return execution
}

func (r *LeasedRuntime) completeLeasedExecution(execution *activeLeasedExecution) {
	if execution == nil {
		return
	}
	execution.complete()
	r.executionsMu.Lock()
	if current := r.executions[execution.key]; current == execution {
		delete(r.executions, execution.key)
	}
	r.executionsMu.Unlock()
}

func (r *LeasedRuntime) leasedExecutions(ref session.SessionRef) []*activeLeasedExecution {
	sessionID := strings.TrimSpace(session.NormalizeSessionRef(ref).SessionID)
	r.executionsMu.Lock()
	defer r.executionsMu.Unlock()
	executions := make([]*activeLeasedExecution, 0, len(r.executions))
	for key, execution := range r.executions {
		if key.sessionID == sessionID && execution != nil {
			executions = append(executions, execution)
		}
	}
	return executions
}

// RecoverLostRun quiesces process-local producers only after their durable
// Session lease is proven expired, absent, or replaced. It does not acquire a
// new lease or repair durable execution state; the next ordinary Runtime.Run
// performs those steps under a fresh fencing token.
func (r *LeasedRuntime) RecoverLostRun(ctx context.Context, ref session.SessionRef) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	executions := r.leasedExecutions(ref)
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

func (g *sessionLeaseGuard) recoveryLoss(ctx context.Context) (error, error) {
	g.mu.Lock()
	lease := g.lease
	loss := g.heartbeatErr
	g.mu.Unlock()
	if loss != nil {
		return loss, nil
	}
	now := g.driver.now()
	if lease.ExpiresAt.IsZero() || !lease.ExpiresAt.After(now) {
		return errSessionLeaseRenewalDeadline, nil
	}
	reader, ok := g.leases.(session.SessionLeaseReader)
	if !ok {
		return nil, nil
	}
	durable, err := reader.SessionLease(ctx, lease.SessionRef)
	if err != nil {
		return nil, fmt.Errorf("controlplane: inspect session lease for lost-run recovery: %w", err)
	}
	if strings.TrimSpace(durable.LeaseID) == "" {
		return errSessionLeaseOwnershipLost, nil
	}
	if durable.LeaseID != lease.LeaseID || durable.OwnerID != lease.OwnerID || durable.FencingToken != lease.FencingToken {
		return errSessionLeaseOwnershipLost, nil
	}
	if durable.Revision < lease.Revision {
		return nil, fmt.Errorf(
			"controlplane: session lease revision moved backwards during lost-run recovery: durable=%d confirmed=%d",
			durable.Revision,
			lease.Revision,
		)
	}
	if durable.ExpiresAt.IsZero() || !durable.ExpiresAt.After(now) {
		return errSessionLeaseRenewalDeadline, nil
	}
	return nil, nil
}

func baseLeasedRunner(runner agent.Runner) *leasedRunner {
	switch runner := runner.(type) {
	case *leasedRunner:
		return runner
	case *leasedSourceRunner:
		return runner.leasedRunner
	default:
		return nil
	}
}
