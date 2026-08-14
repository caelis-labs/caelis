package file

import (
	"context"
	"sync"
)

// contextMutex is a zero-value mutex whose acquisition can be cancelled.
// Request-scoped Store operations use LockContext so callers, including lease
// heartbeats, cannot be trapped behind unrelated local Store work.
type contextMutex struct {
	once  sync.Once
	token chan struct{}
}

func (m *contextMutex) init() {
	m.once.Do(func() {
		m.token = make(chan struct{}, 1)
		m.token <- struct{}{}
	})
}

func (m *contextMutex) Lock() {
	_ = m.LockContext(context.Background())
}

func (m *contextMutex) LockContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.init()
	select {
	case <-m.token:
	case <-ctx.Done():
		return ctx.Err()
	}
	// A cancelled context and an available token are both selectable. Recheck
	// after acquisition so a pre-cancelled request can never win that race and
	// enter a durable Store mutation.
	if err := ctx.Err(); err != nil {
		m.token <- struct{}{}
		return err
	}
	return nil
}

func (m *contextMutex) Unlock() {
	m.init()
	m.token <- struct{}{}
}
