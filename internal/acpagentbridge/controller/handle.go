package controller

import (
	"context"
	"iter"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/acpbridge"
	"github.com/caelis-labs/caelis/internal/eventqueue"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

type turnHandle struct {
	cancelFn  context.CancelFunc
	events    *eventqueue.Queue[turnHandleEvent]
	mu        sync.Mutex
	cancelled bool
	closed    bool
}

type turnHandleEvent struct {
	event acpbridge.SourceEvent
	err   error
}

func newTurnHandle(cancel context.CancelFunc) *turnHandle {
	return &turnHandle{
		cancelFn: cancel,
		events:   eventqueue.New[turnHandleEvent](),
	}
}

func (h *turnHandle) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for {
			item, ok := h.events.Pop()
			if !ok {
				return
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
		for {
			item, ok := h.events.Pop()
			if !ok {
				return
			}
			if !yield(acpbridge.CloneSourceEvent(item.event), item.err) {
				return
			}
		}
	}
}

func (h *turnHandle) Cancel() controller.CancelResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancelled {
		return controller.CancelResult{Status: controller.CancelStatusAlreadyCancelled}
	}
	h.cancelled = true
	if h.cancelFn != nil {
		h.cancelFn()
	}
	return controller.CancelResult{Status: controller.CancelStatusCancelled}
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
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	h.events.Close()
}
