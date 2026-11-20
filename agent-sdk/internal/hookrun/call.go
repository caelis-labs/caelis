// Package hookrun invokes typed SDK extensions without owning execution state.
package hookrun

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

var slots = make(chan struct{}, 32)

func deadline(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}

// Extension contexts inherit cancellation and deadlines, never Runtime-private
// values such as the Session fence bearer or prepared invocation credential.
func privateContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	stop := context.AfterFunc(parent, cancel)
	if d, ok := parent.Deadline(); ok {
		bounded, release := context.WithDeadline(ctx, d)
		return bounded, func() { stop(); release(); cancel() }
	}
	return ctx, func() { stop(); cancel() }
}

func wrap(phase, name string, err error) error {
	if err == nil {
		return nil
	}
	return &agent.HookError{Phase: phase, Name: name, Cause: err}
}

func cloneContext(in agent.HookContext) agent.HookContext {
	in.InputSource = session.CloneActorRef(in.InputSource)
	if in.ToolParent != nil {
		parent := *in.ToolParent
		in.ToolParent = &parent
	}
	if in.ToolDefinition != nil {
		d := tool.CloneDefinition(*in.ToolDefinition)
		in.ToolDefinition = &d
	}
	if in.Tool != nil {
		c := tool.CloneCall(*in.Tool)
		c.Metadata = nil
		in.Tool = &c
	}
	return in
}

// Call bounds outstanding pure transforms. No Store, continuation or authority
// bearer is included in the detached value. Late results cannot update owners.
func Call[I, O any](ctx context.Context, phase string, h agent.Hook[I, O], evidence agent.HookContext, input I, cloneInput func(I) I, cloneOutput func(O) O) (O, error) {
	var zero O
	if err := ctx.Err(); err != nil {
		return zero, wrap(phase, h.Name, err)
	}
	callCtx, cancel := deadline(ctx, h.Timeout)
	defer cancel()
	extensionCtx, release := privateContext(callCtx)
	defer release()
	select {
	case slots <- struct{}{}:
	default:
		return zero, wrap(phase, h.Name, errors.New("outstanding transformation limit reached"))
	}
	type outcome struct {
		value O
		err   error
	}
	done := make(chan outcome, 1)
	input = cloneInput(input)
	evidence = cloneContext(evidence)
	go func() {
		out := outcome{}
		defer func() {
			if p := recover(); p != nil {
				out.err = fmt.Errorf("panic: %v", p)
			}
			<-slots
			done <- out
		}()
		out.value, out.err = h.Apply(extensionCtx, evidence, input)
		if out.err == nil {
			out.value = cloneOutput(out.value)
		}
	}()
	select {
	case <-callCtx.Done():
		return zero, wrap(phase, h.Name, callCtx.Err())
	case out := <-done:
		if err := callCtx.Err(); err != nil {
			return zero, wrap(phase, h.Name, err)
		}
		return out.value, wrap(phase, h.Name, out.err)
	}
}

// Around expires next at return and joins consumed producers. A timed-out or
// uncooperative effect extension cannot cause premature fence/lease release.
func Around[T any](ctx context.Context, phase, name string, timeout time.Duration, invoke func(context.Context, func(context.Context) (T, error)) (T, error), base func(context.Context) (T, error)) (T, error) {
	callCtx, cancel := deadline(ctx, timeout)
	defer cancel()
	extensionCtx, release := privateContext(callCtx)
	defer release()
	var mu sync.Mutex
	var producers sync.WaitGroup
	active, used := true, false
	var violation, producerErr error
	var produced T
	running := false
	next := func(requested context.Context) (T, error) {
		mu.Lock()
		if !active || used || callCtx.Err() != nil {
			violation = errors.New("continuation expired or already consumed")
			err := violation
			mu.Unlock()
			var zero T
			return zero, wrap(phase, name, err)
		}
		used = true
		running = true
		producers.Add(1)
		mu.Unlock()
		defer producers.Done()
		// Preserve the owner's private authority and deadline. A supplied context
		// can reject consumption, but cannot detach the actual producer.
		var out T
		var err error
		if requested != nil && requested.Err() != nil {
			err = requested.Err()
		} else {
			out, err = base(callCtx)
		}
		mu.Lock()
		producerErr = err
		produced = out
		running = false
		mu.Unlock()
		return out, err
	}
	var out T
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("panic: %v", p)
			}
		}()
		out, err = invoke(extensionCtx, next)
	}()
	timeoutErr := errors.Join(callCtx.Err(), extensionCtx.Err())
	mu.Lock()
	active = false
	if running {
		violation = errors.New("continuation outlived its hook")
	}
	mu.Unlock()
	cancel()
	producers.Wait()
	mu.Lock()
	passthrough := violation == nil && timeoutErr == nil && ctx.Err() == nil && producerErr != nil && errors.Is(err, producerErr) && errors.Is(producerErr, err)
	err = errors.Join(err, producerErr, violation)
	if err != nil && used {
		out = produced
	}
	mu.Unlock()
	if passthrough {
		return out, producerErr
	}
	if err == nil {
		err = ctx.Err()
	}
	// Check the deadline before cancellation rather than classifying our own
	// cancellation as a hook timeout.
	if err == nil && timeoutErr != nil {
		err = timeoutErr
	}
	return out, wrap(phase, name, err)
}
