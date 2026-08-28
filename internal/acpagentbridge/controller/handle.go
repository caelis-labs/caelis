package controller

import (
	"context"
	"errors"
	"iter"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/acpbridge"
	"github.com/caelis-labs/caelis/internal/eventqueue"
)

type turnHandle struct {
	cancelFn      context.CancelFunc
	events        *eventqueue.Queue[turnHandleEvent]
	mu            sync.Mutex
	cancelled     bool
	closed        bool
	barrierClosed bool
	barriers      map[*turnHandleBarrier]struct{}
}

type turnHandleEvent struct {
	event   acpbridge.SourceEvent
	err     error
	barrier *turnHandleBarrier
}

type turnHandleBarrier struct {
	once sync.Once
	done chan error
}

func newTurnHandle(cancel context.CancelFunc) *turnHandle {
	return &turnHandle{
		cancelFn: cancel,
		events:   eventqueue.New[turnHandleEvent](),
		barriers: map[*turnHandleBarrier]struct{}{},
	}
}

func (h *turnHandle) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		defer h.closeBarrierAdmission(controller.ErrNotActive)
		for {
			item, ok := h.events.Pop()
			if !ok {
				return
			}
			if item.barrier != nil {
				// The previous yield has returned, so its consumer has completed
				// normalization, persistence, and publication before this ACK.
				h.completeBarrier(item.barrier, nil)
				continue
			}
			if item.err != nil {
				if !yield(nil, item.err) {
					return
				}
				continue
			}
			if item.event.Canonical == nil {
				continue
			}
			if !yield(session.CloneEvent(item.event.Canonical), nil) {
				return
			}
		}
	}
}

func (h *turnHandle) SourceEvents() iter.Seq2[acpbridge.SourceEvent, error] {
	return func(yield func(acpbridge.SourceEvent, error) bool) {
		defer h.closeBarrierAdmission(controller.ErrNotActive)
		for {
			item, ok := h.events.Pop()
			if !ok {
				return
			}
			if item.barrier != nil {
				// The previous yield has returned, so its consumer has completed
				// normalization, persistence, and publication before this ACK.
				h.completeBarrier(item.barrier, nil)
				continue
			}
			if !yield(acpbridge.CloneSourceEvent(item.event), item.err) {
				return
			}
		}
	}
}

func (h *turnHandle) Cancel() controller.CancelResult {
	h.mu.Lock()
	if h.cancelled {
		h.mu.Unlock()
		return controller.CancelResult{Status: controller.CancelStatusAlreadyCancelled}
	}
	h.cancelled = true
	cancelFn := h.cancelFn
	barriers := h.takeBarriersLocked()
	h.mu.Unlock()
	completeTurnHandleBarriers(barriers, context.Canceled)
	if cancelFn != nil {
		cancelFn()
	}
	return controller.CancelResult{Status: controller.CancelStatusCancelled}
}

func (h *turnHandle) Close() error {
	if h != nil {
		h.closeBarrierAdmission(controller.ErrNotActive)
	}
	return nil
}

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
	h.publish(turnHandleEvent{err: err})
}

func (h *turnHandle) publish(item turnHandleEvent) {
	if h == nil {
		return
	}
	h.events.Push(item)
}

func (h *turnHandle) finish() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	barriers := h.takeBarriersLocked()
	h.events.Close()
	h.mu.Unlock()
	completeTurnHandleBarriers(barriers, controller.ErrNotActive)
}

func (h *turnHandle) synchronize(ctx context.Context) error {
	if h == nil {
		return controller.ErrNotActive
	}
	if ctx == nil {
		ctx = context.Background()
	}
	barrier := &turnHandleBarrier{done: make(chan error, 1)}
	h.mu.Lock()
	if h.closed || h.cancelled || h.barrierClosed {
		h.mu.Unlock()
		return controller.ErrNotActive
	}
	if h.barriers == nil {
		h.barriers = map[*turnHandleBarrier]struct{}{}
	}
	h.barriers[barrier] = struct{}{}
	h.events.Push(turnHandleEvent{barrier: barrier})
	h.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-barrier.done:
		return err
	}
}

func (h *turnHandle) completeBarrier(barrier *turnHandleBarrier, err error) {
	if h == nil || barrier == nil {
		return
	}
	h.mu.Lock()
	delete(h.barriers, barrier)
	h.mu.Unlock()
	barrier.complete(err)
}

func (h *turnHandle) failBarriers(err error) {
	if h == nil {
		return
	}
	h.mu.Lock()
	barriers := h.takeBarriersLocked()
	h.mu.Unlock()
	completeTurnHandleBarriers(barriers, err)
}

func (h *turnHandle) closeBarrierAdmission(err error) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.barrierClosed = true
	barriers := h.takeBarriersLocked()
	h.mu.Unlock()
	completeTurnHandleBarriers(barriers, err)
}

func (h *turnHandle) takeBarriersLocked() []*turnHandleBarrier {
	barriers := make([]*turnHandleBarrier, 0, len(h.barriers))
	for barrier := range h.barriers {
		barriers = append(barriers, barrier)
	}
	clear(h.barriers)
	return barriers
}

func (b *turnHandleBarrier) complete(err error) {
	if b == nil {
		return
	}
	b.once.Do(func() {
		if err == nil {
			b.done <- nil
		} else {
			b.done <- err
		}
		close(b.done)
	})
}

func completeTurnHandleBarriers(barriers []*turnHandleBarrier, err error) {
	if err == nil {
		err = errors.New("internal/acpagentbridge/controller: Turn event consumer stopped")
	}
	for _, barrier := range barriers {
		barrier.complete(err)
	}
}
