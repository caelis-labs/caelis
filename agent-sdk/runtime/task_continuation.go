package runtime

import (
	"context"
	"errors"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

// Continuations own the gap between durable submission and producer startup.
// They are independent of a tool-call observer and joined before the Run gives
// up execution authority. Command is the first producer using this boundary.
const maxTaskContinuations = 32

var errTaskAuthorizationChanged = errors.New("task authorization was invalidated by user input")

type taskScopeKey struct{}

type taskContinuationScope struct {
	mu         sync.Mutex
	active     map[string]*taskContinuation
	changed    chan struct{}
	closed     bool
	generation uint64
	err        error
}

type taskContinuation struct {
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelCauseFunc
	claimed  bool
	admitted chan struct{}
	admit    sync.Once
	done     chan struct{}
}

func newTaskContinuationScope() *taskContinuationScope {
	return &taskContinuationScope{active: make(map[string]*taskContinuation), changed: make(chan struct{})}
}

func taskScopeFromContext(ctx context.Context) *taskContinuationScope {
	if ctx == nil {
		return nil
	}
	scope, _ := ctx.Value(taskScopeKey{}).(*taskContinuationScope)
	return scope
}

func (s *taskContinuationScope) currentGeneration() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.generation
}

func (s *taskContinuationScope) register(ctx context.Context, id string, generation uint64) (*taskContinuation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || ctx.Err() != nil {
		return nil, errorcode.New(errorcode.FailedPrecondition, "task execution owner is closed")
	}
	if generation != s.generation {
		return nil, errTaskAuthorizationChanged
	}
	if s.active[id] != nil {
		return nil, errorcode.New(errorcode.Conflict, "task continuation already exists")
	}
	if len(s.active) >= maxTaskContinuations {
		return nil, errorcode.New(errorcode.ResourceExhausted, "too many pending task operations; wait for an existing Task")
	}
	workCtx, cancel := context.WithCancelCause(ctx)
	work := &taskContinuation{ctx: workCtx, cancel: cancel, admitted: make(chan struct{}), done: make(chan struct{})}
	s.active[id] = work
	return work, nil
}

func (s *taskContinuationScope) complete(id string, work *taskContinuation, err error) {
	work.cancel(context.Canceled)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[id] != work {
		return
	}
	delete(s.active, id)
	s.err = errors.Join(s.err, err)
	close(work.done)
	close(s.changed)
	s.changed = make(chan struct{})
}

// revoke and claim serialize cancellation with the durable effect claim. A
// claimed operation has crossed into the producer's existing cancel contract.
func (s *taskContinuationScope) revoke(cause error, closeAdmission bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = s.closed || closeAdmission
	s.generation++
	for _, work := range s.active {
		work.revoke(cause)
	}
}

func (w *taskContinuation) revoke(cause error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.claimed {
		w.cancel(cause)
	}
}

func (w *taskContinuation) live() bool {
	if w == nil {
		return false
	}
	select {
	case <-w.done:
		return false
	default:
		return true
	}
}

func (w *taskContinuation) claim(commit func() error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := context.Cause(w.ctx); err != nil {
		return err
	}
	if w.claimed {
		return errors.New("task execution was already claimed")
	}
	if err := commit(); err != nil {
		return err
	}
	w.claimed = true
	return nil
}

// wait leaves input open while a final safe point joins pending operations.
// Incoming steering wakes it so the existing Chat safe point can consume input.
func (s *taskContinuationScope) wait(ctx context.Context, input <-chan struct{}) bool {
	if s == nil {
		return true
	}
	for {
		s.mu.Lock()
		idle, changed := len(s.active) == 0, s.changed
		s.mu.Unlock()
		if idle {
			return true
		}
		select {
		case <-changed:
		case <-input:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

func (s *taskContinuationScope) join() error {
	if s == nil {
		return nil
	}
	s.wait(context.Background(), nil)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return s.err
}
