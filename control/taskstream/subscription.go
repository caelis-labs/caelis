package taskstream

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

const (
	// A Surface subscription is allowed to absorb ordinary render stalls
	// without forcing a reconnect into Runtime's bounded history. These limits
	// remain fail-fast and per-subscriber so a detached consumer cannot apply
	// backpressure to the authoritative Task producer indefinitely.
	subscriberEventCap = 1024
	subscriberByteCap  = 8 * 1024 * 1024
)

type queuedRecord struct {
	record  Record
	bytes   int
	initial bool
}

type subscription struct {
	ctx    context.Context
	cancel context.CancelFunc
	out    chan Record

	mu             sync.Mutex
	cond           *sync.Cond
	queue          []queuedRecord
	queueBytes     int
	initialPending int
	closed         bool
	err            error
	lastCursor     string
	closeOnce      sync.Once
}

func newSubscription(parent context.Context) *subscription {
	ctx, cancel := context.WithCancel(parent)
	s := &subscription{ctx: ctx, cancel: cancel, out: make(chan Record)}
	s.cond = sync.NewCond(&s.mu)
	go s.deliver()
	go s.closeOnContext()
	return s
}

func (s *subscription) closeOnContext() {
	<-s.ctx.Done()
	s.mu.Lock()
	s.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *subscription) Records() <-chan Record { return s.out }

func (s *subscription) enqueue(record Record) bool {
	return s.enqueueRecord(record, false, false)
}

// enqueueInitial may wait for this subscription's consumer while replaying a
// bounded current-state batch. It runs only on the subscription forwarder, not
// on a Task producer; live delivery continues to fail fast for a slow consumer.
func (s *subscription) enqueueInitial(record Record) bool {
	return s.enqueueRecord(record, true, true)
}

// enqueueCatchup delivers one already bounded snapshot batch before live
// fail-fast delivery resumes. Waiting is local to this Control subscription and
// never blocks the Runtime producer.
func (s *subscription) enqueueCatchup(records []Record) bool {
	for _, record := range records {
		if !s.enqueueInitial(record) {
			return false
		}
	}
	return s.awaitInitialDrain()
}

func (s *subscription) enqueueRecord(record Record, wait bool, initial bool) bool {
	raw, err := json.Marshal(record)
	if err != nil {
		s.finish(err)
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	oversizedInitial := wait && len(raw) > subscriberByteCap
	for wait && !s.closed && (len(s.queue) >= subscriberEventCap || s.queueBytes+len(raw) > subscriberByteCap) {
		// The latest exact Final Message is allowed to be the one queued item
		// larger than the transient delivery byte cap. Waiting cannot make an
		// individually oversized immutable record smaller.
		if len(s.queue) == 0 && oversizedInitial {
			break
		}
		s.cond.Wait()
	}
	if s.closed {
		return false
	}
	if len(s.queue) >= subscriberEventCap ||
		(s.queueBytes+len(raw) > subscriberByteCap && (!oversizedInitial || len(s.queue) != 0)) {
		s.err = ErrSlowConsumer
		s.closed = true
		s.cancel()
		s.cond.Broadcast()
		return false
	}
	s.queue = append(s.queue, queuedRecord{record: cloneRecord(record), bytes: len(raw), initial: initial})
	s.queueBytes += len(raw)
	if initial {
		s.initialPending++
	}
	s.cond.Signal()
	return true
}

// awaitInitialDrain holds the live Runtime subscription at the snapshot
// boundary until every initial record has crossed this subscription's output
// channel. It blocks only the Control forwarder; Task producers remain
// independent and their bounded observation may advance to another gap.
func (s *subscription) awaitInitialDrain() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.initialPending > 0 && !s.closed {
		s.cond.Wait()
	}
	return !s.closed
}

func (s *subscription) deliver() {
	defer close(s.out)
	defer s.cancel()
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
		s.cond.Broadcast()
		s.mu.Unlock()
		select {
		case s.out <- item.record:
			s.mu.Lock()
			s.lastCursor = item.record.Cursor
			if item.initial {
				s.initialPending--
				s.cond.Broadcast()
			}
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
