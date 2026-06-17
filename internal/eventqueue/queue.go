package eventqueue

import "sync"

type Queue[T any] struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []T
	closed bool
}

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

func (q *Queue[T]) Clear() {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	clear(q.items)
	q.items = nil
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
