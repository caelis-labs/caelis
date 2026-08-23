package appserver

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const approvalRecoveryRetryFloor = 25 * time.Millisecond

// ApprovalRecoveryGate lets presentation startup proceed while preserving the
// invariant that no new Turn begins before prior-Host execution fences have
// been retired and abandoned durable approvals have been settled. A sweep
// error is retained and returned to every waiter.
type ApprovalRecoveryGate struct {
	startOnce   sync.Once
	done        chan struct{}
	store       ApprovalRecoveryStore
	authority   approvalRecoveryAuthority
	diagnostics *approvalRecoveryDiagnostics
	workers     sync.WaitGroup

	mu     sync.RWMutex
	err    error
	cancel context.CancelFunc
	closed bool
}

// ApprovalRecoveryGateConfig configures startup recovery. PriorHostFences is a
// Host-owned capability; generic callers leave it nil and cannot take over an
// active execution fence.
type ApprovalRecoveryGateConfig struct {
	Store           ApprovalRecoveryStore
	FenceOwnerID    string
	PriorHostFences PriorHostFenceReplacer
	Diagnostics     *slog.Logger
}

// NewApprovalRecoveryGate constructs a lazy recovery gate. Explicit Start is
// used by interactive startup; Wait also starts the sweep so non-interactive
// Turn entry cannot deadlock or bypass recovery.
func NewApprovalRecoveryGate(config ApprovalRecoveryGateConfig) *ApprovalRecoveryGate {
	return &ApprovalRecoveryGate{
		done:  make(chan struct{}),
		store: config.Store,
		authority: approvalRecoveryAuthority{
			ownerID: config.FenceOwnerID, priorHostFences: config.PriorHostFences,
		},
		diagnostics: newApprovalRecoveryDiagnostics(config.Diagnostics),
	}
}

// Start begins the recovery sweep once. Callers may immediately continue
// presentation assembly; Turn entry points must call Wait.
func (g *ApprovalRecoveryGate) Start(ctx context.Context) {
	if g == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	g.startOnce.Do(func() {
		g.mu.Lock()
		if g.closed {
			g.err = context.Canceled
			g.mu.Unlock()
			close(g.done)
			return
		}
		workerCtx, cancel := context.WithCancel(ctx)
		g.cancel = cancel
		g.workers.Add(1)
		g.mu.Unlock()
		go func() {
			defer g.workers.Done()
			defer cancel()
			started := g.diagnostics.started()
			var result approvalRecoverySweep
			err := sweepPriorHostSessionFences(workerCtx, g.store, g.authority, g.diagnostics)
			if err == nil {
				result, err = sweepAbandonedApprovals(workerCtx, g.store)
			}
			g.mu.Lock()
			g.err = err
			g.mu.Unlock()
			close(g.done)
			// Turn readiness is published before best-effort diagnostics so log
			// file I/O cannot extend the startup gate.
			g.diagnostics.observe(workerCtx, "startup_recovery", started, err)
			if err == nil && !result.retryAt.IsZero() {
				g.retryDeferred(workerCtx, result.retryAt)
			}
		}()
	})
}

func (g *ApprovalRecoveryGate) retryDeferred(ctx context.Context, retryAt time.Time) {
	for !retryAt.IsZero() {
		delay := time.Until(retryAt)
		if delay < approvalRecoveryRetryFloor {
			delay = approvalRecoveryRetryFloor
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
		started := g.diagnostics.started()
		result, err := sweepAbandonedApprovals(ctx, g.store)
		g.diagnostics.observe(ctx, "startup_recovery_retry", started, err)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// The initial gate already completed successfully. Keep deferred
			// cleanup retryable without retroactively wedging unrelated Turns.
			retryAt = time.Now().Add(time.Second)
			continue
		}
		retryAt = result.retryAt
	}
}

// Close cancels the initial sweep or deferred retries and waits until they can
// no longer access the recovery store. The Host must call it before releasing
// the Store directory or other workspace resources.
func (g *ApprovalRecoveryGate) Close() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.closed = true
	cancel := g.cancel
	g.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	g.workers.Wait()
}

// Wait blocks until recovery completes or ctx is canceled.
func (g *ApprovalRecoveryGate) Wait(ctx context.Context) error {
	if g == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	g.Start(context.Background())
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.done:
		g.mu.RLock()
		defer g.mu.RUnlock()
		return g.err
	}
}
