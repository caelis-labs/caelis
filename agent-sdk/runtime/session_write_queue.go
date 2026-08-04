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
	done sync.Once
	wake chan struct{}
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

	ticket := &sessionWriteTicket{wake: make(chan struct{})}
	q.mu.Lock()
	if q.tails == nil {
		q.tails = map[string]*sessionWriteTicket{}
	}
	predecessor := q.tails[key]
	q.tails[key] = ticket
	q.mu.Unlock()

	finish := func() {
		ticket.done.Do(func() {
			close(ticket.wake)
			q.mu.Lock()
			if q.tails[key] == ticket {
				delete(q.tails, key)
			}
			q.mu.Unlock()
		})
	}
	if predecessor == nil {
		return finish, nil
	}
	select {
	case <-predecessor.wake:
		return finish, nil
	case <-ctx.Done():
		// Preserve the registered FIFO chain even when this waiter leaves. Its
		// successor may already be queued behind it, so pass admission onward
		// only after the predecessor completes.
		go func() {
			<-predecessor.wake
			finish()
		}()
		return nil, ctx.Err()
	}
}

func (r *Runtime) acquireSessionWrite(ctx context.Context, ref session.SessionRef) (func(), error) {
	if r == nil {
		return func() {}, nil
	}
	return r.sessionWrites.acquire(ctx, ref)
}
