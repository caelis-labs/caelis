package runtime

import "sync"

type eventQueue[T any] struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []T
	closed bool
}

func newEventQueue[T any]() *eventQueue[T] {
	q := &eventQueue[T]{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *eventQueue[T]) Push(item T) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.items = append(q.items, item)
	q.cond.Signal()
}

func (q *eventQueue[T]) Pop() (T, bool) {
	var zero T
	if q == nil {
		return zero, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.items) == 0 {
		return zero, false
	}
	item := q.items[0]
	copy(q.items, q.items[1:])
	q.items = q.items[:len(q.items)-1]
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
	q.cond.Broadcast()
}
