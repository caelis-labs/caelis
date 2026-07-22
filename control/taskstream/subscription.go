package taskstream

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

const (
	subscriberEventCap = 128
	subscriberByteCap  = 1024 * 1024
)

type queuedRecord struct {
	record Record
	bytes  int
}

type subscription struct {
	ctx    context.Context
	cancel context.CancelFunc
	out    chan Record

	mu         sync.Mutex
	cond       *sync.Cond
	queue      []queuedRecord
	queueBytes int
	closed     bool
	err        error
	lastCursor string
	closeOnce  sync.Once
}

func newSubscription(parent context.Context) *subscription {
	ctx, cancel := context.WithCancel(parent)
	s := &subscription{ctx: ctx, cancel: cancel, out: make(chan Record)}
	s.cond = sync.NewCond(&s.mu)
	go s.deliver()
	return s
}

func (s *subscription) Records() <-chan Record { return s.out }

func (s *subscription) enqueue(record Record) bool {
	raw, err := json.Marshal(record)
	if err != nil {
		s.finish(err)
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if len(s.queue) >= subscriberEventCap || s.queueBytes+len(raw) > subscriberByteCap {
		s.err = ErrSlowConsumer
		s.closed = true
		s.cancel()
		s.cond.Broadcast()
		return false
	}
	s.queue = append(s.queue, queuedRecord{record: cloneRecord(record), bytes: len(raw)})
	s.queueBytes += len(raw)
	s.cond.Signal()
	return true
}

func (s *subscription) deliver() {
	defer close(s.out)
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.closed {
			s.cond.Wait()
		}
		if len(s.queue) == 0 && s.closed {
			s.mu.Unlock()
			return
		}
		item := s.queue[0]
		s.queue[0] = queuedRecord{}
		s.queue = s.queue[1:]
		s.queueBytes -= item.bytes
		s.mu.Unlock()
		select {
		case s.out <- item.record:
			s.mu.Lock()
			s.lastCursor = item.record.Cursor
			s.mu.Unlock()
		case <-s.ctx.Done():
			return
		}
	}
}

func cloneRecord(record Record) Record {
	record.Task.ParentTool = ParentTool{ToolCallID: record.Task.ParentTool.ToolCallID, ToolName: record.Task.ParentTool.ToolName}
	if record.Frame != nil {
		frame := stream.CloneFrame(*record.Frame)
		record.Frame = &frame
	}
	if record.Gap != nil {
		gap := *record.Gap
		record.Gap = &gap
	}
	return record
}

func (s *subscription) finish(err error) {
	cancel := err != nil && !errors.Is(err, context.Canceled)
	s.mu.Lock()
	if s.err == nil && err != nil && !errors.Is(err, context.Canceled) {
		s.err = err
	}
	s.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()
	if cancel {
		s.cancel()
	}
}

func (s *subscription) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		s.finish(nil)
	})
	return nil
}

func (s *subscription) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *subscription) LastCursor() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastCursor
}
