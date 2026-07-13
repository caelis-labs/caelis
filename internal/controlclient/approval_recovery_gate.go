package controlclient

import (
	"context"
	"sync"
)

// ApprovalRecoveryGate lets presentation startup proceed while preserving the
// invariant that no new Turn begins before abandoned durable approvals have
// been settled. A sweep error is retained and returned to every waiter.
type ApprovalRecoveryGate struct {
	startOnce sync.Once
	done      chan struct{}
	store     ApprovalRecoveryStore

	mu  sync.RWMutex
	err error
}

// NewApprovalRecoveryGate constructs a lazy recovery gate. Explicit Start is
// used by interactive startup; Wait also starts the sweep so non-interactive
// Turn entry cannot deadlock or bypass recovery.
func NewApprovalRecoveryGate(store ApprovalRecoveryStore) *ApprovalRecoveryGate {
	return &ApprovalRecoveryGate{done: make(chan struct{}), store: store}
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
		go func() {
			err := SweepAbandonedApprovals(ctx, g.store)
			g.mu.Lock()
			g.err = err
			g.mu.Unlock()
			close(g.done)
		}()
	})
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
