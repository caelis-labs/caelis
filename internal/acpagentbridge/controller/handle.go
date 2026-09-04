package controller

import (
	"context"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/acpbridge"
)

// turnHandle is lifecycle and cancellation only. ACP updates are synchronously
// delivered to the request-installed observer; this handle retains no payload
// queue or replay history.
type turnHandle struct {
	ctx      context.Context
	cancelFn context.CancelFunc
	observer agent.SourceEventObserver

	observerMu sync.Mutex
	mu         sync.Mutex
	cancelled  bool
	finished   bool
	finishErr  error
	done       chan struct{}
	finishOnce sync.Once
}

// turnHandleEvent remains a bounded semantic steering barrier value. It is
// retained only between remote steering acceptance and canonical input commit;
// ordinary turn delivery never passes through this slice.
type turnHandleEvent struct {
	event acpbridge.SourceEvent
	err   error
}

func newTurnHandle(ctx context.Context, cancel context.CancelFunc, observer agent.SourceEventObserver) *turnHandle {
	if ctx == nil {
		ctx = context.Background()
	}
	return &turnHandle{ctx: ctx, cancelFn: cancel, observer: observer, done: make(chan struct{})}
}

func (h *turnHandle) Cancel() controller.CancelResult {
	if h == nil {
		return controller.CancelResult{Status: controller.CancelStatusAlreadyCancelled}
	}
	h.mu.Lock()
	if h.cancelled || h.finished {
		h.mu.Unlock()
		return controller.CancelResult{Status: controller.CancelStatusAlreadyCancelled}
	}
	h.cancelled = true
	cancelFn := h.cancelFn
	h.mu.Unlock()
	if cancelFn != nil {
		cancelFn()
	}
	return controller.CancelResult{Status: controller.CancelStatusCancelled}
}

func (h *turnHandle) WaitCompletion(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-h.done:
		h.mu.Lock()
		err := h.finishErr
		h.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *turnHandle) Close() error { return nil }

func (h *turnHandle) publishEvent(event *session.Event) {
	if h == nil || event == nil {
		return
	}
	h.publish(turnHandleEvent{event: acpbridge.SourceEvent{Canonical: session.CloneEvent(event)}})
}

func (h *turnHandle) publishSourceEvent(event *session.Event, acp *eventstream.Envelope) {
	if h == nil {
		return
	}
	h.publish(turnHandleEvent{event: acpbridge.SourceEvent{
		Canonical: session.CloneEvent(event),
		ACP:       acpbridge.CloneEnvelopePtr(acp),
	}})
}

func (h *turnHandle) publishError(err error) {
	if h == nil || err == nil {
		return
	}
	h.mu.Lock()
	if h.finishErr == nil {
		h.finishErr = err
	}
	h.mu.Unlock()
	h.publish(turnHandleEvent{err: err})
}

func (h *turnHandle) publish(item turnHandleEvent) {
	if h == nil || h.observer == nil {
		return
	}
	event := agent.SourceEvent{Err: item.err}
	if item.err == nil {
		event = agent.SourceEvent{
			Canonical:                        session.CloneEvent(item.event.Canonical),
			Native:                           acpbridge.CloneEnvelopePtr(item.event.ACP),
			CanonicalContentAlreadyPublished: item.event.CanonicalContentAlreadyPublished,
		}
	}
	h.observerMu.Lock()
	err := h.observer.ObserveSourceEvent(h.ctx, event)
	if err != nil {
		// The controller observer owns canonical forwarding, including fenced
		// Session writes. Optional display failures are absorbed by Control.
		h.failBarriers(err)
	}
	h.observerMu.Unlock()
	if err != nil {
		h.Cancel()
	}
}

func (h *turnHandle) finish() {
	if h == nil {
		return
	}
	h.observerMu.Lock()
	h.mu.Lock()
	h.finished = true
	h.mu.Unlock()
	h.observerMu.Unlock()
	h.finishOnce.Do(func() { close(h.done) })
}

// synchronize proves that all updates accepted before the call have completed
// their synchronous observer work. No queue marker is required.
func (h *turnHandle) synchronize(ctx context.Context) error {
	if h == nil {
		return controller.ErrNotActive
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	h.observerMu.Lock()
	h.mu.Lock()
	inactive := h.finished || h.cancelled || h.finishErr != nil
	h.mu.Unlock()
	h.observerMu.Unlock()
	if inactive {
		return controller.ErrNotActive
	}
	return nil
}

func (h *turnHandle) failBarriers(err error) {
	if h == nil || err == nil {
		return
	}
	h.mu.Lock()
	if h.finishErr == nil {
		h.finishErr = err
	}
	h.mu.Unlock()
}
