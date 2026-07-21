package runtime

import "sync"

const runnerEventQueueCapacity = 256

type eventQueue[T any] struct {
	mu       sync.Mutex
	notEmpty *sync.Cond
	items    []T
	head     int
	size     int
	dropped  uint64
	closed   bool
}

type eventQueueDelivery[T any] struct {
	Item    T
	Dropped uint64
}

func newEventQueue[T any]() *eventQueue[T] {
	return newEventQueueWithCapacity[T](runnerEventQueueCapacity)
}

func newEventQueueWithCapacity[T any](capacity int) *eventQueue[T] {
	if capacity <= 0 {
		capacity = 1
	}
	q := &eventQueue[T]{items: make([]T, capacity)}
	q.notEmpty = sync.NewCond(&q.mu)
	return q
}

// Push retains the newest bounded suffix without waiting for a consumer. A
// full queue overwrites its oldest item and reports the resulting observation
// gap through Pop; execution producers must never inherit observer latency.
func (q *eventQueue[T]) Push(item T) bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	if q.size == len(q.items) {
		q.items[q.head] = item
		q.head = (q.head + 1) % len(q.items)
		q.dropped++
	} else {
		index := (q.head + q.size) % len(q.items)
		q.items[index] = item
		q.size++
	}
	q.notEmpty.Signal()
	return true
}

// Pop returns either one item or the non-zero drop count accumulated since the
// previous gap delivery. Clearing that count only when it is returned ensures
// overwrites that race with suffix draining produce a later observable gap.
func (q *eventQueue[T]) Pop() (eventQueueDelivery[T], bool) {
	if q == nil {
		return eventQueueDelivery[T]{}, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.size == 0 && q.dropped == 0 && !q.closed {
		q.notEmpty.Wait()
	}
	if q.dropped > 0 {
		dropped := q.dropped
		q.dropped = 0
		return eventQueueDelivery[T]{Dropped: dropped}, true
	}
	if q.size == 0 {
		return eventQueueDelivery[T]{}, false
	}
	item := q.items[q.head]
	var zero T
	q.items[q.head] = zero
	q.head = (q.head + 1) % len(q.items)
	q.size--
	if q.size == 0 {
		q.head = 0
	}
	return eventQueueDelivery[T]{Item: item}, true
}

func (q *eventQueue[T]) Clear() {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	clear(q.items)
	q.head = 0
	q.size = 0
	q.dropped = 0
}

func (q *eventQueue[T]) Close() {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	q.notEmpty.Broadcast()
}

func (q *eventQueue[T]) Abort() {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	clear(q.items)
	q.head = 0
	q.size = 0
	q.dropped = 0
	q.closed = true
	q.notEmpty.Broadcast()
}
