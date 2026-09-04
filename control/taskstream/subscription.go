package taskstream

import (
	"context"
	"errors"
	"sync"
)

// subscription is an unbuffered reader view. A slow or absent consumer parks
// only this reader goroutine; producers continue appending to the file spool.
type subscription struct {
	ctx    context.Context
	cancel context.CancelFunc
	out    chan Delivery

	mu         sync.Mutex
	err        error
	closeOnce  sync.Once
	finishOnce sync.Once
}

func newSubscription(parent context.Context) *subscription {
	ctx, cancel := context.WithCancel(parent)
	return &subscription{ctx: ctx, cancel: cancel, out: make(chan Delivery)}
}

func (s *subscription) Deliveries() <-chan Delivery { return s.out }

func (s *subscription) deliver(delivery Delivery) bool {
	select {
	case <-s.ctx.Done():
		return false
	case s.out <- delivery:
		return true
	}
}

func (s *subscription) finish(err error) {
	s.finishOnce.Do(func() {
		if err != nil && !errors.Is(err, context.Canceled) {
			s.mu.Lock()
			s.err = err
			s.mu.Unlock()
		}
		close(s.out)
	})
}

func (s *subscription) Close() error {
	s.closeOnce.Do(s.cancel)
	return nil
}

func (s *subscription) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
