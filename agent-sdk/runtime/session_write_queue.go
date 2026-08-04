package runtime

import (
	"context"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// sessionWriteQueue admits multi-stage Session mutations in arrival order.
// It is a logical transaction queue, not a persistence lock: store revision
// checks and Runtime lease fences remain authoritative across Runtime instances.
type sessionWriteQueue struct {
	mu    sync.Mutex
	tails map[string]*sessionWriteTicket
}

type sessionWriteTicket struct {
	done     sync.Once
	ready    chan struct{}
	previous *sessionWriteTicket
	next     *sessionWriteTicket
	admitted bool
}

func (q *sessionWriteQueue) acquire(ctx context.Context, ref session.SessionRef) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := strings.TrimSpace(session.NormalizeSessionRef(ref).SessionID)
	if key == "" {
		return func() {}, nil
	}

	ticket := &sessionWriteTicket{ready: make(chan struct{})}
	q.mu.Lock()
	if q.tails == nil {
		q.tails = map[string]*sessionWriteTicket{}
	}
	predecessor := q.tails[key]
	ticket.previous = predecessor
	if predecessor != nil {
		predecessor.next = ticket
	} else {
		ticket.admitted = true
		close(ticket.ready)
	}
	q.tails[key] = ticket
	q.mu.Unlock()

	finish := func() {
		ticket.done.Do(func() {
			q.mu.Lock()
			q.finishLocked(key, ticket)
			q.mu.Unlock()
		})
	}
	if predecessor == nil {
		return finish, nil
	}
	select {
	case <-ticket.ready:
		return finish, nil
	case <-ctx.Done():
		// Retire the ticket synchronously. If admission raced with
		// cancellation, advance its successor; otherwise unlink it from the
		// pending chain without leaving a goroutine waiting on its predecessor.
		ticket.done.Do(func() {
			q.mu.Lock()
			if ticket.admitted {
				q.finishLocked(key, ticket)
			} else {
				q.unlinkLocked(key, ticket)
			}
			q.mu.Unlock()
		})
		return nil, ctx.Err()
	}
}

func (q *sessionWriteQueue) finishLocked(key string, ticket *sessionWriteTicket) {
	if ticket == nil {
		return
	}
	next := ticket.next
	if next != nil {
		next.previous = nil
		next.admitted = true
		close(next.ready)
	} else if q.tails[key] == ticket {
		delete(q.tails, key)
	}
	ticket.previous = nil
	ticket.next = nil
}

func (q *sessionWriteQueue) unlinkLocked(key string, ticket *sessionWriteTicket) {
	if ticket == nil {
		return
	}
	previous := ticket.previous
	next := ticket.next
	if previous != nil {
		previous.next = next
	}
	if next != nil {
		next.previous = previous
	} else if q.tails[key] == ticket {
		if previous == nil {
			delete(q.tails, key)
		} else {
			q.tails[key] = previous
		}
	}
	ticket.previous = nil
	ticket.next = nil
}

func (r *Runtime) acquireSessionWrite(ctx context.Context, ref session.SessionRef) (func(), error) {
	if r == nil {
		return func() {}, nil
	}
	return r.sessionWrites.acquire(ctx, ref)
}
