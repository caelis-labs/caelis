package jsonrpc

import (
	"context"
	"sync"
)

// writerArbiter gives every JSON-RPC writer one FIFO admission point while
// allowing a call that has not written bytes to withdraw on cancellation.
type writerArbiter struct {
	mu      sync.Mutex
	held    bool
	waiters []*writerWaiter
}

type writerWaiter struct {
	ready   chan struct{}
	granted bool
}

func newWriterArbiter() *writerArbiter {
	return &writerArbiter{}
}

func (a *writerArbiter) acquire(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a == nil {
		return func() {}, nil
	}

	waiter := &writerWaiter{ready: make(chan struct{})}
	a.mu.Lock()
	if !a.held && len(a.waiters) == 0 {
		a.held = true
		waiter.granted = true
		a.mu.Unlock()
		return a.releaseFunc(), nil
	}
	a.waiters = append(a.waiters, waiter)
	a.mu.Unlock()

	select {
	case <-waiter.ready:
		if err := ctx.Err(); err != nil {
			a.release()
			return nil, err
		}
		return a.releaseFunc(), nil
	case <-ctx.Done():
		a.mu.Lock()
		if waiter.granted {
			a.mu.Unlock()
			a.release()
			return nil, ctx.Err()
		}
		for i, candidate := range a.waiters {
			if candidate == waiter {
				a.waiters = append(a.waiters[:i], a.waiters[i+1:]...)
				break
			}
		}
		a.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (a *writerArbiter) releaseFunc() func() {
	var once sync.Once
	return func() {
		once.Do(a.release)
	}
}

func (a *writerArbiter) release() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for len(a.waiters) > 0 {
		next := a.waiters[0]
		a.waiters = a.waiters[1:]
		if next == nil || next.granted {
			continue
		}
		next.granted = true
		close(next.ready)
		return
	}
	a.held = false
}
