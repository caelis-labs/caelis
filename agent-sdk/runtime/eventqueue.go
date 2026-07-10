package runtime

import "sync"

const runnerEventQueueCapacity = 256

type eventQueue[T any] struct {
	mu       sync.Mutex
	notEmpty *sync.Cond
	notFull  *sync.Cond
	items    []T
	capacity int
	closed   bool
}

func newEventQueue[T any]() *eventQueue[T] {
	return newEventQueueWithCapacity[T](runnerEventQueueCapacity)
}

func newEventQueueWithCapacity[T any](capacity int) *eventQueue[T] {
	if capacity <= 0 {
		capacity = 1
	}
	q := &eventQueue[T]{capacity: capacity}
	q.notEmpty = sync.NewCond(&q.mu)
	q.notFull = sync.NewCond(&q.mu)
	return q
}

func (q *eventQueue[T]) Push(item T) bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) >= q.capacity && !q.closed {
		q.notFull.Wait()
	}
	if q.closed {
		return false
	}
	q.items = append(q.items, item)
	q.notEmpty.Signal()
	return true
}

func (q *eventQueue[T]) Pop() (T, bool) {
	var zero T
	if q == nil {
		return zero, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && !q.closed {
		q.notEmpty.Wait()
	}
	if len(q.items) == 0 {
		return zero, false
	}
	item := q.items[0]
	copy(q.items, q.items[1:])
	var zeroItem T
	q.items[len(q.items)-1] = zeroItem
	q.items = q.items[:len(q.items)-1]
	q.notFull.Signal()
	return item, true
}

func (q *eventQueue[T]) Clear() {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	clear(q.items)
	q.items = nil
	q.notFull.Broadcast()
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
	q.notFull.Broadcast()
}

func (q *eventQueue[T]) Abort() {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	clear(q.items)
	q.items = nil
	q.closed = true
	q.notEmpty.Broadcast()
	q.notFull.Broadcast()
}
