package file

import (
	"context"
	"sync"
)

// contextMutex is a zero-value mutex whose acquisition can be cancelled.
// Ordinary Store operations use Lock; lease operations use LockContext so a
// heartbeat deadline cannot be trapped behind unrelated local Store work.
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
	m.init()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-m.token:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *contextMutex) Unlock() {
	m.init()
	m.token <- struct{}{}
}
