package controlplane

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const defaultSessionLeaseTTL = 30 * time.Second

// LeasedRuntimeConfig configures the Control-owned placement guard around one
// execution Runtime. The lease covers the asynchronous Runner lifetime.
type LeasedRuntimeConfig struct {
	Runtime           agent.Runtime
	Leases            session.SessionLeaseService
	OwnerID           string
	TTL               time.Duration
	HeartbeatInterval time.Duration
}

// LeasedRuntime acquires a store-level session lease before dispatch and keeps
// it alive until the returned Runner completes or closes.
type LeasedRuntime struct {
	runtime           agent.Runtime
	leases            session.SessionLeaseService
	ownerID           string
	ttl               time.Duration
	heartbeatInterval time.Duration
}

func NewLeasedRuntime(config LeasedRuntimeConfig) (*LeasedRuntime, error) {
	if config.Runtime == nil {
		return nil, fmt.Errorf("controlplane: leased runtime requires an execution runtime")
	}
	if config.Leases == nil {
		return nil, fmt.Errorf("controlplane: leased runtime requires a session lease service")
	}
	ownerID := strings.TrimSpace(config.OwnerID)
	if ownerID == "" {
		return nil, fmt.Errorf("controlplane: leased runtime requires owner_id")
	}
	ttl := config.TTL
	if ttl <= 0 {
		ttl = defaultSessionLeaseTTL
	}
	interval := config.HeartbeatInterval
	if interval <= 0 {
		interval = ttl / 3
	}
	if interval <= 0 || interval >= ttl {
		return nil, fmt.Errorf("controlplane: lease heartbeat interval must be positive and less than TTL")
	}
	return &LeasedRuntime{runtime: config.Runtime, leases: config.Leases, ownerID: ownerID, ttl: ttl, heartbeatInterval: interval}, nil
}

func (r *LeasedRuntime) Run(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
	ref := session.NormalizeSessionRef(req.SessionRef)
	lease, err := r.leases.AcquireSessionLease(ctx, session.AcquireSessionLeaseRequest{SessionRef: ref, OwnerID: r.ownerID, TTL: r.ttl})
	if err != nil {
		return agent.RunResult{}, err
	}
	ctx = session.ContextWithRuntimeLease(ctx, lease)
	result, err := r.runtime.Run(ctx, req)
	if err != nil {
		return agent.RunResult{}, errors.Join(err, r.release(lease))
	}
	if result.Handle == nil {
		return result, r.release(lease)
	}
	result.Handle = newLeasedRunner(result.Handle, r.leases, lease, r.ttl, r.heartbeatInterval)
	return result, nil
}

func (r *LeasedRuntime) RunState(ctx context.Context, ref session.SessionRef) (agent.RunState, error) {
	return r.runtime.RunState(ctx, ref)
}

func (r *LeasedRuntime) release(lease session.SessionLease) error {
	ctx, cancel := context.WithTimeout(context.Background(), min(r.ttl, 5*time.Second))
	defer cancel()
	err := r.leases.ReleaseSessionLease(ctx, session.ReleaseSessionLeaseRequest{
		SessionRef: lease.SessionRef, LeaseID: lease.LeaseID, OwnerID: lease.OwnerID, ExpectedLeaseRevision: lease.Revision,
	})
	if session.IsCommitted(err) {
		return nil
	}
	return err
}

type leasedRunner struct {
	inner    agent.Runner
	leases   session.SessionLeaseService
	ttl      time.Duration
	interval time.Duration

	mu           sync.Mutex
	lease        session.SessionLease
	heartbeatErr error
	stop         chan struct{}
	finishOnce   sync.Once
	finishErr    error
	wg           sync.WaitGroup
}

func newLeasedRunner(inner agent.Runner, leases session.SessionLeaseService, lease session.SessionLease, ttl, interval time.Duration) agent.Runner {
	runner := &leasedRunner{inner: inner, leases: leases, lease: lease, ttl: ttl, interval: interval, stop: make(chan struct{})}
	runner.wg.Add(1)
	go runner.heartbeat()
	if source, ok := inner.(agent.SourceHandle); ok {
		return &leasedSourceRunner{leasedRunner: runner, source: source}
	}
	return runner
}

func (r *leasedRunner) RunID() string { return r.inner.RunID() }

func (r *leasedRunner) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		completed := true
		for event, err := range r.inner.Events() {
			if !yield(event, err) {
				completed = false
				break
			}
		}
		if !completed {
			_ = r.inner.Close()
		}
		if err := errors.Join(r.currentHeartbeatError(), r.finish()); err != nil {
			yield(nil, err)
		}
	}
}

type leasedSourceRunner struct {
	*leasedRunner
	source agent.SourceHandle
}

func (r *leasedSourceRunner) SourceEvents() iter.Seq2[agent.SourceEvent, error] {
	return func(yield func(agent.SourceEvent, error) bool) {
		completed := true
		for event, err := range r.source.SourceEvents() {
			if !yield(event, err) {
				completed = false
				break
			}
		}
		if !completed {
			_ = r.inner.Close()
		}
		if err := errors.Join(r.currentHeartbeatError(), r.finish()); err != nil {
			yield(agent.SourceEvent{}, err)
		}
	}
}

func (r *leasedRunner) Submit(submission agent.Submission) error { return r.inner.Submit(submission) }

func (r *leasedRunner) Cancel() agent.CancelResult { return r.inner.Cancel() }

func (r *leasedRunner) Close() error { return errors.Join(r.inner.Close(), r.finish()) }

func (r *leasedRunner) heartbeat() {
	defer r.wg.Done()
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			if err := r.heartbeatOnce(); err != nil {
				r.mu.Lock()
				r.heartbeatErr = err
				r.mu.Unlock()
				r.inner.Cancel()
				return
			}
		}
	}
}

func (r *leasedRunner) heartbeatOnce() error {
	r.mu.Lock()
	lease := r.lease
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), r.interval)
	next, err := r.leases.HeartbeatSessionLease(ctx, session.HeartbeatSessionLeaseRequest{
		SessionRef: lease.SessionRef, LeaseID: lease.LeaseID, OwnerID: lease.OwnerID,
		ExpectedLeaseRevision: lease.Revision, TTL: r.ttl,
	})
	cancel()
	if session.IsCommitted(err) {
		if next.LeaseID == lease.LeaseID && next.Revision > lease.Revision {
			err = nil
		} else if reader, ok := r.leases.(session.SessionLeaseReader); ok {
			ctx, cancel = context.WithTimeout(context.Background(), r.interval)
			next, err = reader.SessionLease(ctx, lease.SessionRef)
			cancel()
		}
	}
	if err != nil {
		return fmt.Errorf("controlplane: session lease heartbeat failed: %w", err)
	}
	if next.LeaseID != lease.LeaseID {
		return fmt.Errorf("controlplane: session lease identity changed during heartbeat")
	}
	r.mu.Lock()
	r.lease = next
	r.mu.Unlock()
	return nil
}

func (r *leasedRunner) currentHeartbeatError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.heartbeatErr
}

func (r *leasedRunner) finish() error {
	r.finishOnce.Do(func() {
		close(r.stop)
		r.wg.Wait()
		r.mu.Lock()
		lease := r.lease
		r.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), min(r.ttl, 5*time.Second))
		defer cancel()
		r.finishErr = r.leases.ReleaseSessionLease(ctx, session.ReleaseSessionLeaseRequest{
			SessionRef: lease.SessionRef, LeaseID: lease.LeaseID, OwnerID: lease.OwnerID, ExpectedLeaseRevision: lease.Revision,
		})
		if session.IsCommitted(r.finishErr) {
			r.finishErr = nil
		}
	})
	return r.finishErr
}
