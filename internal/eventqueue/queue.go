package eventqueue

import "sync"

type Queue[T any] struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []T
	head   int
	closed bool
}

const compactThreshold = 1024

func New[T any]() *Queue[T] {
	q := &Queue[T]{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *Queue[T]) Push(item T) {
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

func (q *Queue[T]) Pop() (T, bool) {
	var zero T
	if q == nil {
		return zero, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.head == len(q.items) && !q.closed {
		q.cond.Wait()
	}
	if q.head == len(q.items) {
		return zero, false
	}
	item := q.items[q.head]
	q.items[q.head] = zero
	q.head++
	q.compactLocked()
	return item, true
}

func (q *Queue[T]) compactLocked() {
	if q.head == len(q.items) {
		q.items = q.items[:0]
		q.head = 0
		return
	}
	if q.head < compactThreshold || q.head < len(q.items)-q.head {
		return
	}
	remaining := copy(q.items, q.items[q.head:])
	clear(q.items[remaining:])
	q.items = q.items[:remaining]
	q.head = 0
}

func (q *Queue[T]) Clear() {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	clear(q.items)
	q.items = nil
	q.head = 0
}

func (q *Queue[T]) Close() {
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
