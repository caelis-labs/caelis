package gatewayapp

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/controlplane"
	"github.com/google/uuid"
)

// idleCompactionCoordinator belongs to one activation. Foreground signals only
// cancel: they never wait for a provider, acquire a Store lock, or settle usage.
// The Host-wide slot limits generation, not accounting identities.
type idleCompactionCoordinator struct {
	composition  *runtimeComposition
	engine       *runtime.Runtime
	activationID string
	dwell        time.Duration
	mu           sync.Mutex
	current      *idleCompactionWork
	closing      bool
}
type idleCompactionJob interface {
	Fresh() bool
	Run(context.Context, session.IdleCompactionWindow) (bool, error)
	FlushAccounting(context.Context) error
	Close()
}
type idleCompactionWork struct {
	job         idleCompactionJob
	ctx         context.Context
	cancel      context.CancelFunc
	released    chan session.IdleCompactionWindow
	done        chan struct{}
	releaseWork func()
}

func (c *idleCompactionCoordinator) prepare(ctx context.Context, ref session.SessionRef, fence session.SessionFence) (*controlplane.IdleCompactionHandoff, error) {
	c.mu.Lock()
	unavailable := c.closing || c.current != nil
	c.mu.Unlock()
	if unavailable || c.composition.closing.Load() {
		c.engine.DiscardIdleCompactionSource(ref)
		return nil, nil
	}
	job, err := c.engine.PrepareIdleCompaction(ctx, ref)
	if err != nil || job == nil {
		return nil, err
	}
	releaseWork, ok := c.composition.retainIdleWork(ref)
	if !ok {
		job.Close()
		return nil, nil
	}
	lifecycle := c.composition.authorities.lifecycleCtx
	if lifecycle == nil {
		lifecycle = context.Background()
	}
	workCtx, cancel := context.WithCancel(lifecycle)
	work := &idleCompactionWork{job: job, ctx: workCtx, cancel: cancel, released: make(chan session.IdleCompactionWindow, 1), done: make(chan struct{}), releaseWork: releaseWork}
	c.mu.Lock()
	if c.closing || c.current != nil {
		c.mu.Unlock()
		cancel()
		job.Close()
		releaseWork()
		return nil, nil
	}
	c.current = work
	c.mu.Unlock()
	go c.run(work)
	source := job.Source(fence.OwnerID, c.activationID)
	return &controlplane.IdleCompactionHandoff{Release: session.NewIdleCompactionRelease(fence, source), Complete: func(window session.IdleCompactionWindow, err error) {
		if err != nil {
			cancel()
		}
		work.released <- window
	}}, nil
}

func (c *idleCompactionCoordinator) run(work *idleCompactionWork) {
	defer func() {
		work.cancel()
		work.job.Close()
		c.mu.Lock()
		if c.current == work {
			c.current = nil
		}
		c.mu.Unlock()
		work.releaseWork()
		close(work.done)
	}()
	// Keep the dedicated work reference until the original release loop has
	// observed its transition, including cancellation during an uncertain write.
	window := <-work.released
	if window.SessionRef().SessionID == "" {
		return
	}
	store := c.composition.sessions.(session.IdleCompactionStore)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := store.RevokeIdleCompaction(ctx, window); err != nil {
			c.report("revoke", err)
		}
	}()
	timer := time.NewTimer(c.dwell)
	defer timer.Stop()
	select {
	case <-work.ctx.Done():
		return
	case <-timer.C:
	}
	quiet, err := c.composition.idleQuiet(work.ctx, window.SessionRef())
	if err != nil {
		c.report("quiet", err)
	}
	if err != nil || !quiet || !work.job.Fresh() || work.ctx.Err() != nil {
		return
	}
	slot := c.composition.authorities.idleGeneration
	select {
	case slot <- struct{}{}:
	default:
		return
	}
	_, err = work.job.Run(work.ctx, window)
	<-slot
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, session.ErrIdleCompactionRevoked) {
		c.report("run", err)
	}
	// This retry path exists only after a real accounting failure. It retries
	// sealed original IDs, never generation. The workRef pins dependencies even
	// after cancellation, so a close timeout cannot tear them down underneath it.
	delay := time.Second
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = work.job.FlushAccounting(ctx)
		cancel()
		if err == nil {
			return
		}
		c.report("accounting_pending", err)
		retry := time.NewTimer(delay)
		<-retry.C
		if delay < 5*time.Second {
			delay = min(5*time.Second, delay*2)
		}
	}
}

func (c *idleCompactionCoordinator) cancel() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.current != nil {
		c.current.cancel()
	}
	c.mu.Unlock()
}
func (c *idleCompactionCoordinator) stop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.closing = true
	if c.current != nil {
		c.current.cancel()
	}
	c.mu.Unlock()
}
func (c *idleCompactionCoordinator) drain(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.stop()
	c.mu.Lock()
	current := c.current
	c.mu.Unlock()
	if current != nil {
		select {
		case <-current.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if c.engine != nil {
		c.engine.DiscardIdleCompactionSources()
	}
	return nil
}
func (c *idleCompactionCoordinator) report(phase string, err error) {
	if logger := c.composition.authorities.diagnostics; logger != nil {
		logger.Warn("idle compaction", "phase", phase, "activation", c.activationID, "error", err)
	}
}

func (s *runtimeComposition) newIdleCompactionCoordinator(engine *runtime.Runtime) *idleCompactionCoordinator {
	if s.retainIdleWork == nil || s.idleQuiet == nil || s.authorities.idleGeneration == nil {
		return nil
	}
	return &idleCompactionCoordinator{composition: s, engine: engine, activationID: uuid.NewString(), dwell: 60 * time.Second}
}

func (r *sessionRuntimeRegistry) idleCompactionQuiet(ctx context.Context, runtime *sessionRuntime, ref session.SessionRef) (bool, error) {
	r.mu.RLock()
	quiet := !r.closed && !runtime.releasing && r.sessions[ref.SessionID] == runtime && runtime.inUse == 0 && runtime.workRefs == 1
	r.mu.RUnlock()
	if !quiet {
		return false, nil
	}
	running, err := r.hasRunningTasks(ctx, ref.SessionID)
	if err != nil || running {
		return false, err
	}
	if reader, ok := r.sessionService.(session.ApprovalRecoveryReader); ok {
		pending, err := reader.PendingApprovals(ctx)
		if err != nil {
			return false, err
		}
		for _, p := range pending {
			if p.SessionRef.SessionID == ref.SessionID {
				return false, nil
			}
		}
	}
	return true, nil
}

// recoverIdleCompactionAccounting is a startup-only use of the existing
// exclusive Host capability. No activation, Turn or provider call is created.
func recoverIdleCompactionAccounting(ctx context.Context, sessions session.Service, capability session.PriorHostFenceService, owner string) error {
	recovery, ok := capability.(session.PriorHostCompactionReceiptRecovery)
	if !ok {
		return nil
	}
	cursor := ""
	for {
		listed, err := sessions.ListSessions(ctx, session.ListSessionsRequest{Cursor: cursor, Limit: 200})
		if err != nil {
			return err
		}
		for _, entry := range listed.Sessions {
			if err := recovery.RecoverCompactionReceipts(ctx, session.SessionRef{SessionID: entry.SessionID}, owner); err != nil && !session.IsCommitted(err) {
				return err
			}
		}
		if listed.NextCursor == "" {
			return nil
		}
		if listed.NextCursor == cursor {
			return errors.New("idle accounting recovery listing did not advance")
		}
		cursor = listed.NextCursor
	}
}
